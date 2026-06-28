package gossip

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"

	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
)

// Active-push раздаваемого seed'ом obf (per-node CPS-пакет I1, мимикрия под QUIC+SNI).
// В отличие от пассивного gossip-pull, seed САМ POST'ит каждой ноде её I1 по туннелю и
// получает прямой ACK (ответ 204) — «seed сказал → нода применила». I1 не рвёт туннель
// (initiator-local), поэтому раздача безопасна и не требует синхронности. Приём монотонен
// и идемпотентен (mesh.ShouldAdoptObf); применение к device — в reconciler по ObfVersion.

// maxObfBody — лимит тела POST /v1/obf. I1-spec — десятки-сотни байт hex, 4 КиБ с запасом.
const maxObfBody = 4 << 10

// ObfPush — wire-форма раздачи: версия + уже готовый I1-spec (SNI зашит seed'ом внутрь).
// Нода применяет I1 как есть, ничего не генерит.
type ObfPush struct {
	Version uint64 `json:"version"`
	I1      string `json:"i1"`
}

// PushObf активно отправляет ноде на meshIP:port её obf. Возврат nil = нода приняла (или
// уже держит ≥ этой версии — идемпотентно): это и есть прямой ACK для seed-push-цикла.
func PushObf(ctx context.Context, hc *http.Client, meshIP string, port int, push ObfPush) error {
	body, err := json.Marshal(push)
	if err != nil {
		return err
	}
	reqURL := fmt.Sprintf("http://%s:%d/v1/obf", meshIP, port)
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
		return fmt.Errorf("obf push: status %d", resp.StatusCode)
	}
	return nil
}

// handleObf принимает seed-push obf и сохраняет его монотонно. Источник обязан быть
// seed'ом (obf раздаёт только он); приём идемпотентен (та же/старее версия → no-op, тоже
// 204 — это валидный ACK). На wg-device I1 применяет отдельный reconciler по ObfVersion;
// здесь только store, чтобы не связывать gossip с device (как и handleTombstone).
func (s *Server) handleObf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var push ObfPush
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxObfBody)).Decode(&push); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if push.Version == 0 || push.I1 == "" {
		http.Error(w, "bad obf push", http.StatusBadRequest)
		return
	}
	st, err := s.store.Read()
	if err != nil {
		s.log.Error("load state failed", "err", err)
		http.Error(w, "state unavailable", http.StatusInternalServerError)
		return
	}
	srcIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !seedAuthorized(st.Peers, srcIP) {
		s.log.Warn("rejected obf-push from non-seed", "src", srcIP)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if _, err := s.store.Update(func(st *state.State) error {
		if !mesh.ShouldAdoptObf(st.ObfVersion, push.Version) {
			return state.ErrNoChange // уже держим эту или новее — идемпотентно
		}
		st.LocalObf.I1 = push.I1
		st.ObfVersion = push.Version
		return nil
	}); err != nil {
		s.log.Error("apply pushed obf failed", "err", err)
		http.Error(w, "state error", http.StatusInternalServerError)
		return
	}
	s.log.Info("obf applied via seed-push", "version", push.Version)
	w.WriteHeader(http.StatusNoContent)
}

// seedAuthorized — push легитимен, только если источник = mesh-IP seed-пира (obf раздаёт
// исключительно seed). Пустой/неизвестный srcIP → отказ (fail-closed).
func seedAuthorized(peers []state.Peer, srcIP string) bool {
	if srcIP == "" {
		return false
	}
	for _, p := range peers {
		if p.IsSeed && p.NodeIP == srcIP {
			return true
		}
	}
	return false
}
