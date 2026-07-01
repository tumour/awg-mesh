package api

import (
	"net/http"
	"time"

	"github.com/tumour/awg-mesh/internal/mesh"
)

// handleStatus — GET /api/v1/status. StatusView (self + peers), обогащённый
// live-сигналом wg-handshake (если источник доступен).
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.Read()
	if err != nil {
		s.log.Error("read state failed", "err", err)
		internalError(w, s.log)
		return
	}
	writeJSON(w, s.log, http.StatusOK, mesh.BuildStatusLive(st, s.liveStats(), time.Now()))
}

// handlePeers — GET /api/v1/peers. Коллекция PeerView (тот же источник, что и
// в status; отдельный ресурс — под будущую пагинацию/фильтры).
func (s *Server) handlePeers(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.Read()
	if err != nil {
		s.log.Error("read state failed", "err", err)
		internalError(w, s.log)
		return
	}
	writeJSON(w, s.log, http.StatusOK, mesh.BuildStatusLive(st, s.liveStats(), time.Now()).Peers)
}

// liveStats — снапшот live-сигнала или nil при отсутствии/ошибке источника.
// Live-обогащение необязательно: отказ провайдера (device недоступен) деградирует
// к state-only, а не роняет статус в 500.
func (s *Server) liveStats() map[string]mesh.PeerLive {
	if s.stats == nil {
		return nil
	}
	live, err := s.stats()
	if err != nil {
		s.log.Warn("live stats unavailable", "err", err)
		return nil
	}
	return live
}

// handleHealth — GET /api/v1/health. State-derived роллап (без сетевых probe'ов).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.Read()
	if err != nil {
		s.log.Error("read state failed", "err", err)
		internalError(w, s.log)
		return
	}
	writeJSON(w, s.log, http.StatusOK, mesh.BuildHealth(st))
}

// handleNotFound — канонический JSON-404 для нераспознанных путей (catch-all "/").
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	notFound(w, s.log)
}
