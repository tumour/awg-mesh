package gossip

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// Client — periodic gossip-puller. Каждый tick выбирает random peer'а из
// текущего state и делает GET /v1/peers через wg-туннель.
type Client struct {
	statePath string
	selfPub   string // base64 — фильтруем себя из gossip-целей
	interval  time.Duration
	port      int
	http      *http.Client
	// onNewPeers вызывается с новыми peer'ами для пуша в wg-device.
	onNewPeers func(peers []state.Peer)
}

// NewClient создаёт gossip-клиента. onNewPeers — callback для wg-device-update.
func NewClient(
	statePath string,
	selfPub string,
	interval time.Duration,
	port int,
	onNewPeers func([]state.Peer),
) *Client {
	return &Client{
		statePath:  statePath,
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
			c.doRound()
		}
	}
}

// doRound — один цикл: выбрать peer'а, опросить, merge.
func (c *Client) doRound() {
	st, err := state.Load(c.statePath)
	if err != nil {
		log.Printf("gossip: load state: %v", err)
		return
	}

	target := pickRandomPeer(st.Peers, c.selfPub)
	if target == nil {
		// Нет peer'ов кроме себя — нечего гoссипить.
		return
	}

	resp, err := c.fetchPeers(target.NodeIP)
	if err != nil {
		log.Printf("gossip: fetch from %s (%s): %v", target.Label, target.NodeIP, err)
		return
	}

	newPeers := mergePeers(st.Peers, resp.Peers, c.selfPub)
	if len(newPeers) == 0 {
		return // ничего нового
	}

	// Сохраняем обновлённый state и применяем к wg-device.
	st.Peers = append(st.Peers, newPeers...)
	if err := st.Save(c.statePath); err != nil {
		log.Printf("gossip: save state: %v", err)
		return
	}
	log.Printf("gossip: learned %d new peers from %s", len(newPeers), target.Label)

	if c.onNewPeers != nil {
		c.onNewPeers(newPeers)
	}
}

// fetchPeers — HTTP GET /v1/peers через wg-туннель.
func (c *Client) fetchPeers(meshIP string) (*PeersResponse, error) {
	url := fmt.Sprintf("http://%s:%d/v1/peers", meshIP, c.port)
	req, err := http.NewRequest(http.MethodGet, url, nil)
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

// mergePeers — возвращает peer'ов из remote, которых нет в local (по pubkey).
// Себя из remote всегда отфильтровываем.
func mergePeers(local []state.Peer, remote []PeerInfo, selfPub string) []state.Peer {
	known := make(map[string]bool, len(local))
	for _, p := range local {
		known[p.PublicKey] = true
	}
	var added []state.Peer
	for _, r := range remote {
		if r.PublicKey == selfPub {
			continue
		}
		if known[r.PublicKey] {
			continue
		}
		added = append(added, state.Peer{
			Label:     r.Label,
			PublicKey: r.PublicKey,
			Endpoint:  r.Endpoint,
			NodeIP:    r.NodeIP,
			IsSeed:    r.IsSeed,
			LastSeen:  time.Now().UTC(),
		})
	}
	return added
}
