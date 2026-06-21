package gossip

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/tumour/awg-mesh/internal/mesh"
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
}

// NewClient создаёт gossip-клиента. onNewPeers — callback для wg-device-update.
func NewClient(
	store *state.Store,
	selfPub string,
	interval time.Duration,
	port int,
	onNewPeers func([]state.Peer),
) *Client {
	return &Client{
		store:      store,
		selfPub:    selfPub,
		interval:   interval,
		port:       port,
		http:       &http.Client{Timeout: 10 * time.Second},
		onNewPeers: onNewPeers,
	}
}

// Run — gossip-loop. Останавливается при отмене ctx.
func (c *Client) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	log.Printf("gossip: client started (interval=%s)", c.interval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("gossip: client stopped")
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
		log.Printf("gossip: load state: %v", err)
		return
	}

	target := pickRandomPeer(st.Peers, c.selfPub)
	if target == nil {
		// Нет peer'ов кроме себя — нечего гoссипить.
		return
	}

	resp, err := c.fetchPeers(ctx, target.NodeIP)
	if err != nil {
		log.Printf("gossip: fetch from %s (%s): %v", target.Label, target.NodeIP, err)
		return
	}

	// Merge — внутри Update, против свежего state: между fetch'ем и записью
	// bootstrap-listener мог зарегистрировать нового peer'а, merge поверх
	// устаревшего снапшота потерял бы его.
	// Конвертируем wire-тип gossip.PeerInfo → доменный state.Peer: mesh-домен
	// не знает про gossip/proto-форматы.
	remote := make([]state.Peer, 0, len(resp.Peers))
	for _, r := range resp.Peers {
		remote = append(remote, state.Peer{
			Label:     r.Label,
			PublicKey: r.PublicKey,
			Endpoint:  r.Endpoint,
			NodeIP:    r.NodeIP,
			IsSeed:    r.IsSeed,
		})
	}

	var changed []state.Peer
	if _, err := c.store.Update(func(s *state.State) error {
		merged, ch := mesh.MergePeers(s.Peers, remote, c.selfPub)
		if len(ch) == 0 {
			return state.ErrNoChange // ничего нового — не перезаписываем файл
		}
		s.Peers = merged
		changed = ch
		return nil
	}); err != nil {
		log.Printf("gossip: save state: %v", err)
		return
	}
	if len(changed) == 0 {
		return
	}
	log.Printf("gossip: %d peers added/updated (from %s)", len(changed), target.Label)

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

// pickRandomPeer возвращает случайного peer'а из local-state кроме нас самих.
func pickRandomPeer(peers []state.Peer, selfPub string) *state.Peer {
	candidates := make([]state.Peer, 0, len(peers))
	for _, p := range peers {
		if p.PublicKey == selfPub {
			continue
		}
		// Не гoссипим к peer'ам без NodeIP (некорректное состояние).
		if p.NodeIP == "" {
			continue
		}
		candidates = append(candidates, p)
	}
	if len(candidates) == 0 {
		return nil
	}
	picked := candidates[rand.IntN(len(candidates))]
	return &picked
}

// Доменный merge peer-list'а живёт в internal/mesh.MergePeers — вызывается
// из doRound выше (после конверсии gossip.PeerInfo → state.Peer).
