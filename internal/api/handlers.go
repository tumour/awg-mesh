package api

import (
	"net/http"

	"github.com/tumour/awg-mesh/internal/mesh"
)

// handleStatus — GET /api/v1/status. Полный StatusView (self + peers).
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.Read()
	if err != nil {
		s.log.Error("read state failed", "err", err)
		internalError(w, s.log)
		return
	}
	writeJSON(w, s.log, http.StatusOK, mesh.BuildStatus(st))
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
	writeJSON(w, s.log, http.StatusOK, mesh.BuildStatus(st).Peers)
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
