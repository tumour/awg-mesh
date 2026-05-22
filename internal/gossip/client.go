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

	merged, changed := mergePeers(st.Peers, resp.Peers, c.selfPub)
	if len(changed) == 0 {
		return // ничего нового и ничего не изменилось
	}

	// Заменяем peer-list полностью (мог обновиться endpoint существующих).
	st.Peers = merged
	if err := st.Save(c.statePath); err != nil {
		log.Printf("gossip: save state: %v", err)
		return
	}
	log.Printf("gossip: %d peers added/updated (from %s)", len(changed), target.Label)

	if c.onNewPeers != nil {
		c.onNewPeers(changed)
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

// mergePeers — мерж peer-list'а с remote-ноды через gossip.
//
// Возвращает (merged, changed):
//   merged — полный новый список для state.Peers (с обновлёнными endpoint'ами
//            существующих peer'ов и refresh'нутыми LastSeen).
//   changed — что надо пушнуть в wg-device через UpdatePeer (новые peers + те
//            у кого изменился endpoint). Pure refresh LastSeen в changed не идёт —
//            wg-device от этого не зависит.
//
// Себя из remote всегда отфильтровываем (selfPub). Себя из local — сохраняем
// как есть. Удаление peer'ов (revoke / tombstone) — v0.2, сейчас union-with-update.
func mergePeers(local []state.Peer, remote []PeerInfo, selfPub string) (merged []state.Peer, changed []state.Peer) {
	// Индекс remote по pubkey, заодно отфильтровываем себя.
	rByKey := make(map[string]PeerInfo, len(remote))
	for _, r := range remote {
		if r.PublicKey == selfPub {
			continue
		}
		rByKey[r.PublicKey] = r
	}

	now := time.Now().UTC()
	localKeys := make(map[string]bool, len(local))

	// Проход по local — обновляем endpoint/label/IsSeed если remote знает свежее.
	for _, p := range local {
		localKeys[p.PublicKey] = true
		if p.PublicKey == selfPub {
			merged = append(merged, p)
			continue
		}
		r, ok := rByKey[p.PublicKey]
		if !ok {
			// Remote не знает — оставляем как есть, LastSeen не refresh'аем
			// (мы её не подтвердили этим раундом).
			merged = append(merged, p)
			continue
		}
		updated := p
		updated.LastSeen = now
		endpointChanged := r.Endpoint != "" && r.Endpoint != p.Endpoint
		if endpointChanged {
			updated.Endpoint = r.Endpoint
		}
		if r.Label != "" && r.Label != p.Label {
			updated.Label = r.Label
		}
		if r.IsSeed != p.IsSeed {
			updated.IsSeed = r.IsSeed
		}
		merged = append(merged, updated)
		// В wg-device пушим только при endpoint-смене — label/IsSeed на wg не влияют.
		if endpointChanged {
			changed = append(changed, updated)
		}
	}

	// Новые peers — те что есть в remote, но не в local.
	for _, r := range remote {
		if r.PublicKey == selfPub {
			continue
		}
		if localKeys[r.PublicKey] {
			continue
		}
		newP := state.Peer{
			Label:     r.Label,
			PublicKey: r.PublicKey,
			Endpoint:  r.Endpoint,
			NodeIP:    r.NodeIP,
			IsSeed:    r.IsSeed,
			LastSeen:  now,
		}
		merged = append(merged, newP)
		changed = append(changed, newP)
	}

	return merged, changed
}
