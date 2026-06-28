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

// Active-push раздаваемой seed'ом flag-day-смены СЕТЕВЫХ params (S/H/J). В отличие от
// пассивного gossip-pull, seed САМ POST'ит каждой ноде текущий Pending по туннелю и
// получает ack ПРЯМО в ответе (announce/commit-версии) — «seed сказал → нода приняла →
// seed узнал». Это снимает livelock и strand: подтверждения собираются напрямую, а не
// случайным pull'ом. Приём Pending монотонен (mesh.ShouldAdoptPending), применение к
// device (flip в ApplyAt) — отдельный paramFlipper, как и раньше.
//
// Pending намеренно дублируется и в пассивном /v1/peers (резервная доставка committed
// ApplyAt): нода вернее держит ApplyAt → флипает синхронно со всеми, не застревая.

// maxParamBody — лимит тела POST /v1/params. Pending = Params (десятки полей) + версия
// + время; 16 КиБ с запасом против флуда гигантским телом (DoS на память роутера).
const maxParamBody = 16 << 10

// ParamPush — wire-форма раздачи flag-day: текущий Pending (анонс или committed).
// Нода применяет его монотонно и отвечает ParamAck со своими версиями.
type ParamPush struct {
	Pending *state.PendingParams `json:"pending"`
}

// ParamAck — ответ ноды на ParamPush: версии, которые она держит. Announce — высшая,
// чей АНОНС нода видела; Commit — высшая, для которой держит COMMITTED ApplyAt (анонс
// не считается committed). Расхождение этих двух и есть защита от strand'а медленной
// ноды: seed коммитит ApplyAt по announce-ack, «вооружает» flip по commit-ack.
type ParamAck struct {
	Announce uint64 `json:"announce"`
	Commit   uint64 `json:"commit"`
}

// PushParams активно отправляет ноде на meshIP:port текущий Pending и возвращает её
// ack. Ошибка (сеть/не-200) = нода не подтвердила на этом тике — ретрай на следующем.
func PushParams(ctx context.Context, hc *http.Client, meshIP string, port int, push ParamPush) (ParamAck, error) {
	body, err := json.Marshal(push)
	if err != nil {
		return ParamAck{}, err
	}
	reqURL := fmt.Sprintf("http://%s:%d/v1/params", meshIP, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return ParamAck{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return ParamAck{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ParamAck{}, fmt.Errorf("param push: status %d", resp.StatusCode)
	}
	var ack ParamAck
	if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
		return ParamAck{}, fmt.Errorf("decode param ack: %w", err)
	}
	return ack, nil
}

// handleParams принимает seed-push Pending, адоптит его монотонно (ShouldAdoptPending —
// тот же домен, что и при пассивном pull) и ВСЕГДА отвечает 200 со своим ack (даже на
// идемпотентный повтор: seed обязан получить подтверждение, иначе flag-day зависнет).
// Источник обязан быть seed'ом (flag-day раздаёт только он, как и obf).
func (s *Server) handleParams(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var push ParamPush
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxParamBody)).Decode(&push); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if push.Pending == nil {
		http.Error(w, "nil pending", http.StatusBadRequest)
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
		s.log.Warn("rejected param-push from non-seed", "src", srcIP)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	updated, err := s.store.Update(func(st *state.State) error {
		if !mesh.ShouldAdoptPending(st.ParamsVersion, st.Pending, push.Pending) {
			return state.ErrNoChange // уже держим эту/новее или это не новее применённого — идемпотентно
		}
		st.Pending = push.Pending
		return nil
	})
	if err != nil {
		s.log.Error("apply pushed pending failed", "err", err)
		http.Error(w, "state error", http.StatusInternalServerError)
		return
	}
	ack := ParamAck{
		Announce: mesh.AnnounceAckVersion(updated),
		Commit:   mesh.CommitAckVersion(updated),
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(ack); err != nil {
		s.log.Warn("encode param ack failed", "err", err)
	}
}
