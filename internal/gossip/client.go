package gossip

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
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
	// onNewPeers вызывается с новыми peer'ами для пуша в wg-device.
	onNewPeers func(peers []state.Peer)
	log        *slog.Logger
}

// NewClient создаёт gossip-клиента. onNewPeers — callback для wg-device-update.
// logger (nil → slog.Default()) инъектируется для embeddability.
func NewClient(
	store *state.Store,
	selfPub string,
	interval time.Duration,
	port int,
	onNewPeers func([]state.Peer),
	logger *slog.Logger,
) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		store:      store,
		selfPub:    selfPub,
		interval:   interval,
		port:       port,
		http:       &http.Client{Timeout: 10 * time.Second},
		onNewPeers: onNewPeers,
		log:        logger.With("component", "gossip"),
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
	target := pickRandomPeer(mesh.GossipCandidates(st))
	if target == nil {
		// Нет достижимых peer'ов кроме себя — нечего гoссипить.
		return
	}

	resp, err := c.fetchPeers(ctx, target.NodeIP)
	if err != nil {
		c.log.Warn("fetch peers failed", "peer", target.Label, "mesh_ip", target.NodeIP, "err", err)
		return
	}

	// Merge — внутри Update, против свежего state: между fetch'ем и записью
	// bootstrap-listener мог зарегистрировать нового peer'а, merge поверх
	// устаревшего снапшота потерял бы его. Конвертируем wire → домен: mesh.MergePeers
	// работает на state.Peer и не знает про wire-форматы.
	remote := proto.PeerInfosToState(resp.Peers)

	var changed []state.Peer
	var rejected []string
	if _, err := c.store.Update(func(s *state.State) error {
		merged, ch, rej, persist := mesh.MergePeers(s.Peers, remote, c.selfPub, s.NetworkCIDR)
		rejected = rej
		changed = ch
		// Пишем файл по persist (значимое изменение для диска), НЕ по changed
		// (что пушить в device): label/IsSeed-обновления значимы для state, но в
		// changed не попадают — без этого они бы молча терялись.
		if !persist {
			return state.ErrNoChange // только LastSeen-refresh — файл не трогаем
		}
		s.Peers = merged
		return nil
	}); err != nil {
		c.log.Warn("merge/save state failed", "err", err)
		return
	}
	// Отказы — потенциальные попытки угона mesh-IP/маршрута соседом; видно оператору.
	for _, r := range rejected {
		c.log.Warn("gossip peer rejected", "reason", r, "from", target.Label)
	}
	if len(changed) == 0 {
		return
	}
	c.log.Info("peers added/updated", "count", len(changed), "from", target.Label)

	if c.onNewPeers != nil {
		c.onNewPeers(changed)
	}
}

// fetchPeers — HTTP GET /v1/peers через wg-туннель. ctx прокидывается, чтобы
// летящий запрос обрывался при shutdown, не дожидаясь HTTP-таймаута.
func (c *Client) fetchPeers(ctx context.Context, meshIP string) (*PeersResponse, error) {
	url := fmt.Sprintf("http://%s:%d/v1/peers", meshIP, c.port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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

// pickRandomPeer возвращает случайного peer'а из готового списка кандидатов
// (фильтрация себя/достижимости — в mesh.GossipCandidates). nil если список пуст.
func pickRandomPeer(candidates []state.Peer) *state.Peer {
	if len(candidates) == 0 {
		return nil
	}
	picked := candidates[rand.IntN(len(candidates))]
	return &picked
}

// Доменный merge peer-list'а живёт в internal/mesh.MergePeers — вызывается
// из doRound выше (после конверсии proto.PeerInfo → state.Peer).
