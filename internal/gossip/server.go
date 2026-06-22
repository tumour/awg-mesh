// Package gossip — periodic peer-list synchronization между нодами через wg-туннель.
//
// Сервер биндится ИСКЛЮЧИТЕЛЬНО на mesh-IP (например 100.64.0.1:9100), не на
// 0.0.0.0. С eth0/публичного интерфейса к нему не достучаться — единственный
// путь это через wg-туннель. Trust-by-tunneling: внутри wg все peers уже
// прошли Noise IKpsk2 с правильным cluster-secret, доверяем им.
//
// Wire-protocol: HTTP JSON. Endpoint:
//
//	GET /v1/peers → {"peers":[{label,public_key,endpoint,node_ip,is_seed},...]}
//
// Клиент мерджит ответ со своим peer-list'ом и применяет diff к wg-device.
package gossip

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
)

// DefaultPort — порт gossip-API. Слушает только на mesh-IP.
const DefaultPort = 9100

// HTTP-таймауты gossip-сервера: даже внутри доверенного туннеля медленный/
// зависший peer не должен держать соединение и goroutine бесконечно (slowloris).
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
)

// Server — HTTP API для отдачи peer-listа.
type Server struct {
	store *state.Store
	addr  string
	srv   *http.Server
	log   *slog.Logger
}

// PeersResponse — JSON-форма ответа на /v1/peers. Peers — общий wire-DTO
// proto.PeerInfo (тот же, что в bootstrap-HelloResponse).
type PeersResponse struct {
	Peers     []proto.PeerInfo `json:"peers"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// NewServer создаёт сервер на mesh-IP:port. host обычно = state.NodeIP.
// logger (nil → slog.Default()) инъектируется для embeddability.
func NewServer(host string, port int, store *state.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	return &Server{store: store, addr: addr, log: logger.With("component", "gossip")}
}

// Start запускает сервер. Останавливается при отмене ctx.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/peers", s.handlePeers)

	s.srv = &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
	}()

	s.log.Info("server listening", "url", fmt.Sprintf("http://%s/v1/peers", s.addr))
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
	st, err := s.store.Read()
	if err != nil {
		s.log.Error("load state failed", "err", err)
		http.Error(w, "state unavailable", http.StatusInternalServerError)
		return
	}

	resp := PeersResponse{
		Peers:     proto.PeerInfosFromState(st.Peers),
		UpdatedAt: st.UpdatedAt,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Статус 200 уже ушёл (заголовок записан) — починить тело нельзя, но факт
		// битого ответа фиксируем у себя.
		s.log.Warn("encode peers response failed", "err", err)
	}
}
