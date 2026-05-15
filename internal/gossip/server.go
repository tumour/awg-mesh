// Package gossip — periodic peer-list synchronization между нодами через wg-туннель.
//
// Сервер биндится ИСКЛЮЧИТЕЛЬНО на mesh-IP (например 100.64.0.1:9100), не на
// 0.0.0.0. С eth0/публичного интерфейса к нему не достучаться — единственный
// путь это через wg-туннель. Trust-by-tunneling: внутри wg все peers уже
// прошли Noise IKpsk2 с правильным cluster-secret, доверяем им.
//
// Wire-protocol: HTTP JSON. Endpoint:
//   GET /v1/peers → {"peers":[{label,public_key,endpoint,node_ip,is_seed},...]}
//
// Клиент мерджит ответ со своим peer-list'ом и применяет diff к wg-device.
package gossip

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// DefaultPort — порт gossip-API. Слушает только на mesh-IP.
const DefaultPort = 9100

// Server — HTTP API для отдачи peer-listа.
type Server struct {
	statePath string
	addr      string
	srv       *http.Server
}

// PeersResponse — JSON-форма ответа на /v1/peers.
type PeersResponse struct {
	Peers     []PeerInfo `json:"peers"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// PeerInfo — описание peer'а в gossip-ответе.
type PeerInfo struct {
	Label     string `json:"label"`
	PublicKey string `json:"public_key"`
	Endpoint  string `json:"endpoint,omitempty"`
	NodeIP    string `json:"node_ip"`
	IsSeed    bool   `json:"is_seed"`
}

// NewServer создаёт сервер на mesh-IP:port. host обычно = state.NodeIP.
func NewServer(host string, port int, statePath string) *Server {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	return &Server{statePath: statePath, addr: addr}
}

// Start запускает сервер. Останавливается при отмене ctx.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/peers", s.handlePeers)

	s.srv = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
	}()

	log.Printf("gossip: listening on http://%s/v1/peers", s.addr)
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("gossip server: %w", err)
	}
	return nil
}

// handlePeers — отдаёт текущий state.peers как JSON.
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	st, err := state.Load(s.statePath)
	if err != nil {
		log.Printf("gossip: load state: %v", err)
		http.Error(w, "state unavailable", http.StatusInternalServerError)
		return
	}

	peers := make([]PeerInfo, 0, len(st.Peers))
	for _, p := range st.Peers {
		peers = append(peers, PeerInfo{
			Label:     p.Label,
			PublicKey: p.PublicKey,
			Endpoint:  p.Endpoint,
			NodeIP:    p.NodeIP,
			IsSeed:    p.IsSeed,
		})
	}
	resp := PeersResponse{Peers: peers, UpdatedAt: st.UpdatedAt}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
