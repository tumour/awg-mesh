// Package api — read-only control-API поверх state для web-морды (и мониторинга).
//
// Как gossip, сервер биндится ИСКЛЮЧИТЕЛЬНО на mesh-IP (100.64.0.X:9110), не на
// 0.0.0.0: с публичного интерфейса недоступен, единственный путь — через
// wg-туннель. Trust-by-tunneling: внутри wg все прошли Noise IKpsk2 с правильным
// cluster-secret. Для read-only это безопасно — StatusView/HealthView без секретов.
// Поднимается ТОЛЬКО на seed (см. node.Run): web-морда живёт на seed.
//
// Тонкий транспорт: домен и маппинг в DTO — в internal/mesh (BuildStatus/
// BuildHealth), здесь только роутинг, конверты, middleware и HTTP-таймауты.
//
// Wire-protocol: HTTP JSON под конвертом {"data":...} / {"error":...}. Роуты:
//
//	GET /api/v1/status → полный StatusView (self + peers)
//	GET /api/v1/peers  → коллекция PeerView
//	GET /api/v1/health → HealthView (state-derived роллап)
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
)

// LiveStatsFunc — источник live-сигнала (pubkey → mesh.PeerLive) для обогащения
// статуса wg-handshake'ом. nil → API отдаёт state-only (без live_status).
// Инъектируется из node (замыкание над wg-device). Ошибку возвращает наверх —
// handler деградирует к state-only, а не роняет ответ в 500.
type LiveStatsFunc func() (map[string]mesh.PeerLive, error)

// DefaultPort — порт control-API. Слушает только на mesh-IP (рядом с gossip 9100).
const DefaultPort = 9110

// HTTP-таймауты: те же соображения, что у gossip — даже в доверенном туннеле
// медленный/зависший клиент не должен держать соединение и goroutine (slowloris).
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 5 * time.Second
)

// Server — read-only control-API поверх state.
type Server struct {
	store *state.Store
	stats LiveStatsFunc // nil → API без live-обогащения (state-only)
	addr  string
	srv   *http.Server
	log   *slog.Logger
}

// NewServer создаёт API-сервер на host:port (host обычно = state.NodeIP).
// stats (nil → state-only) — источник live-сигнала. logger (nil → slog.Default())
// инъектируется для тестируемости/embeddability.
func NewServer(host string, port int, store *state.Store, stats LiveStatsFunc, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		store: store,
		stats: stats,
		addr:  net.JoinHostPort(host, fmt.Sprintf("%d", port)),
		log:   logger.With("component", "api"),
	}
}

// Handler собирает маршруты и middleware в http.Handler. Выделен отдельно от
// Start, чтобы тесты гоняли роутер через httptest без реального listener'а.
//
// Пути регистрируем БЕЗ метода в паттерне: метод проверяем в get()-обёртке, чтобы
// 405/404 отдавались нашим JSON-конвертом, а не дефолтным text/plain ServeMux'а.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/status", s.get(s.handleStatus))
	mux.HandleFunc("/api/v1/peers", s.get(s.handlePeers))
	mux.HandleFunc("/api/v1/health", s.get(s.handleHealth))
	mux.HandleFunc("/", s.handleNotFound) // catch-all → JSON-404
	return s.recoverPanic(s.logRequests(mux))
}

// Start поднимает listener и обслуживает до отмены ctx.
func (s *Server) Start(ctx context.Context) error {
	s.srv = &http.Server{
		Addr:              s.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
	}()

	s.log.Info("server listening", "url", fmt.Sprintf("http://%s/api/v1/status", s.addr))
	if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("api server: %w", err)
	}
	return nil
}
