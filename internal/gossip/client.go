package gossip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
)

// Client — periodic gossip-puller. Каждый tick выбирает random peer'а из
// текущего state и делает GET /v1/peers через wg-туннель.
type Client struct {
	store    *state.Store
	selfPub  string // base64 — фильтруем себя из gossip-целей
	interval time.Duration
	port     int
	http     *http.Client
	// onNewPeers вызывается с новыми/обновлёнными peer'ами для пуша в wg-device.
	onNewPeers func(peers []state.Peer)
	// onRemovedPeers вызывается с отозванными (tombstone) peer'ами — снять с
	// wg-device через RemovePeer (на лету, без рестарта).
	onRemovedPeers func(peers []state.Peer)
	// selector выбирает цель опроса с учётом runtime-доступности: стабильно-
	// недостижимый endpoint-пир уходит в backoff, чтобы не жечь ½ циклов на таймаут.
	selector *targetSelector
	log      *slog.Logger
}

// NewClient создаёт gossip-клиента. onNewPeers/onRemovedPeers — callback'и для
// wg-device: добавить/обновить и снять (revoke) peer'ов соответственно.
// logger (nil → slog.Default()) инъектируется для embeddability.
func NewClient(
	store *state.Store,
	selfPub string,
	interval time.Duration,
	port int,
	onNewPeers func([]state.Peer),
	onRemovedPeers func([]state.Peer),
	logger *slog.Logger,
) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		store:          store,
		selfPub:        selfPub,
		interval:       interval,
		port:           port,
		http:           &http.Client{Timeout: 10 * time.Second},
		onNewPeers:     onNewPeers,
		onRemovedPeers: onRemovedPeers,
		selector:       newTargetSelector(interval),
		log:            logger.With("component", "gossip"),
	}
}

// Run — gossip-loop. Останавливается при отмене ctx.
func (c *Client) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	c.log.Info("client started", "interval", c.interval)

	for {
		select {
		case <-ctx.Done():
			c.log.Info("client stopped")
			return
		case <-ticker.C:
			c.doRound(ctx)
		}
	}
}

// doRound — один цикл: выбрать peer'а, опросить, merge.
func (c *Client) doRound(ctx context.Context) {
	st, err := c.store.Read()
	if err != nil {
		c.log.Warn("load state failed", "err", err)
		return
	}

	// Кандидаты = достижимые пиры (доменное правило mesh.GossipCandidates):
	// не долбимся к узлу, к которому by-design нет пути (оба за NAT) — иначе
	// гарантированный таймаут + каскад wg junk-retry в логах, без обмена данными.
	// selector ещё и исключает тех, кто фейлит ПРЯМО СЕЙЧАС (backoff), чтобы не
	// терять циклы на стабильно-недостижимый endpoint-пир.
	now := time.Now()
	target := c.selector.pick(mesh.GossipCandidates(st), now)
	if target == nil {
		// Нет достижимых peer'ов кроме себя — нечего гoссипить.
		return
	}

	// flag-day-подтверждения нода отдаёт НЕ здесь: seed собирает их прямым active-push'ем
	// (POST /v1/params). Pull остаётся резервным каналом ДОСТАВКИ Pending (адопт ниже).
	resp, err := c.fetchPeers(ctx, target.NodeIP)
	if err != nil {
		c.selector.recordFailure(target.PublicKey, now)
		c.log.Warn("fetch peers failed", "peer", target.Label, "mesh_ip", target.NodeIP, "err", err)
		return
	}
	c.selector.recordSuccess(target.PublicKey)

	// Merge — внутри Update, против свежего state: между fetch'ем и записью
	// bootstrap-listener мог зарегистрировать нового peer'а, merge поверх
	// устаревшего снапшота потерял бы его. Конвертируем wire → домен: mesh.MergePeers
	// работает на state.Peer и не знает про wire-форматы.
	remote := proto.PeerInfosToState(resp.Peers)

	var changed, removed []state.Peer
	var rejected []string
	var adoptedPending *state.PendingParams
	var newTombstones int
	if _, err := c.store.Update(func(s *state.State) error {
		// Сначала мерджим отзывы: MergePeers ниже должен видеть актуальный набор
		// tombstones, чтобы выкинуть отозванных из local И не воскресить из remote.
		mergedTomb, addedTomb := mesh.MergeTombstones(s.Tombstones, resp.Tombstones)
		newTombstones = len(addedTomb)

		merged, ch, rej, persist := mesh.MergePeers(s.Peers, remote, mergedTomb, c.selfPub, s.NetworkCIDR)
		rejected = rej
		changed = ch
		// removed — те, кто РЕАЛЬНО был у нас и теперь отозван: их надо снять с
		// wg-device. Считаем от старого s.Peers (не от merged) — иначе в removed
		// попали бы реанонсы из remote, которых на device и не было. Идемпотентно:
		// в следующем цикле отозванного уже нет в s.Peers → removed пуст.
		_, removed = mesh.ApplyTombstones(s.Peers, mergedTomb, c.selfPub)

		// Принять запланированную смену params, если она строго новее нашей
		// (домен решает монотонность). Trust-by-tunneling: внутри wg-туннеля
		// источник уже прошёл cluster-secret-проверку.
		if mesh.ShouldAdoptPending(s.ParamsVersion, s.Pending, resp.Pending) {
			s.Pending = resp.Pending
			adoptedPending = resp.Pending
		}
		// Пишем файл по любому значимому изменению: peers (persist, включая удаление
		// отозванных), принятый Pending, новые tombstones. Чистый LastSeen-refresh
		// (persist=false) файл не трогает — иначе flash-wear каждый gossip-цикл.
		if !persist && adoptedPending == nil && len(addedTomb) == 0 {
			return state.ErrNoChange
		}
		s.Peers = merged
		s.Tombstones = mergedTomb
		return nil
	}); err != nil {
		c.log.Warn("merge/save state failed", "err", err)
		return
	}
	if adoptedPending != nil {
		c.log.Info("pending params adopted", "version", adoptedPending.Version,
			"apply_at", adoptedPending.ApplyAt, "from", target.Label)
	}
	if newTombstones > 0 {
		c.log.Info("tombstones adopted", "count", newTombstones, "from", target.Label)
	}
	// Отказы — попытки угона mesh-IP/маршрута ИЛИ заблокированный реанонс отозванного.
	for _, r := range rejected {
		c.log.Warn("gossip peer rejected", "reason", r, "from", target.Label)
	}
	// Снятие отозванных с wg-device (RemovePeer) — на лету, без рестарта демона.
	if len(removed) > 0 {
		if c.onRemovedPeers != nil {
			c.onRemovedPeers(removed)
		}
		c.log.Info("peers revoked", "count", len(removed), "from", target.Label)
	}
	if len(changed) > 0 {
		c.log.Info("peers added/updated", "count", len(changed), "from", target.Label)
		if c.onNewPeers != nil {
			c.onNewPeers(changed)
		}
	}
}

// fetchPeers — HTTP GET /v1/peers через wg-туннель. ctx прокидывается, чтобы
// летящий запрос обрывался при shutdown, не дожидаясь HTTP-таймаута.
func (c *Client) fetchPeers(ctx context.Context, meshIP string) (*PeersResponse, error) {
	reqURL := fmt.Sprintf("http://%s:%d/v1/peers", meshIP, c.port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var out PeersResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	return &out, nil
}

// PushTombstone отправляет один tombstone на /v1/tombstone цели (через wg-туннель).
// Используется meshd leave: уходящая нода — особенно за NAT, которую никто не пуллит
// (mesh.GossipCandidates) — сама пушит свой отзыв endpoint-пирам, иначе он не
// разойдётся. Best-effort: caller логирует исход по каждому пиру. hc инъектируется,
// чтобы вызывать из CLI без полного Client.
func PushTombstone(ctx context.Context, hc *http.Client, meshIP string, port int, ts state.Tombstone) error {
	body, err := json.Marshal(ts)
	if err != nil {
		return err
	}
	reqURL := fmt.Sprintf("http://%s:%d/v1/tombstone", meshIP, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// Доменный merge peer-list'а живёт в internal/mesh.MergePeers — вызывается
// из doRound выше (после конверсии proto.PeerInfo → state.Peer).
