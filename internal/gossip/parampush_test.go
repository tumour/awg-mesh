package gossip

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
)

// seedPeers — peer-list ноды: seed на seedIP (авторизован пушить) + сама нода.
func seedPeers(seedIP, selfKey, selfIP string) []state.Peer {
	return []state.Peer{
		{Label: "seed", PublicKey: "SEEDKEY", NodeIP: seedIP, IsSeed: true},
		{Label: "self", PublicKey: selfKey, NodeIP: selfIP},
	}
}

// pushParamsReq — POST /v1/params с телом push от источника srcIP.
func pushParamsReq(t *testing.T, srv *Server, srcIP string, push ParamPush) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(push)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/params", bytes.NewReader(body))
	req.RemoteAddr = srcIP + ":40000"
	rec := httptest.NewRecorder()
	srv.handleParams(rec, req)
	return rec
}

// handleParams адоптит СТРОГО более новый Pending от seed'а и возвращает ack с
// announce-версией; commit-ack у анонса (ApplyAt=0) = применённой версии (анонс НЕ
// committed) — иначе seed решит, что нода готова флипать, не получив ApplyAt.
func TestHandleParams_AdoptsAnnounceAndAcks(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "SELF", NodeIP: "100.64.0.2",
		ParamsVersion: 4, Peers: seedPeers("100.64.0.1", "SELF", "100.64.0.2"),
	})
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

	announce := &state.PendingParams{Version: 5, Params: awgparams.Params{S4: 16}} // ApplyAt=0
	rec := pushParamsReq(t, srv, "100.64.0.1", ParamPush{Pending: announce})

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	var ack ParamAck
	if err := json.NewDecoder(rec.Body).Decode(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.Announce != 5 {
		t.Errorf("announce-ack = %d, want 5 (нода видела анонс)", ack.Announce)
	}
	if ack.Commit != 4 {
		t.Errorf("commit-ack = %d, want 4 (анонс НЕ committed → применённая версия)", ack.Commit)
	}
	s, _ := store.Read()
	if s.Pending == nil || s.Pending.Version != 5 || s.Pending.Params.S4 != 16 {
		t.Fatalf("announce не адоптирован: %+v", s.Pending)
	}
}

// handleParams подхватывает committed ApplyAt на УЖЕ принятый announced-Pending той же
// версии (announced→committed) и репортит commit-ack = этой версии. Это сценарий,
// дважды клавший сеть: без распространения перехода флипал только seed.
func TestHandleParams_AdoptsCommitOnAnnounced(t *testing.T) {
	applyAt := time.Now().Add(2 * time.Minute).UTC()
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "SELF", NodeIP: "100.64.0.2",
		ParamsVersion: 4,
		Pending:       &state.PendingParams{Version: 5, Params: awgparams.Params{S4: 16}}, // announced
		Peers:         seedPeers("100.64.0.1", "SELF", "100.64.0.2"),
	})
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

	committed := &state.PendingParams{Version: 5, ApplyAt: applyAt, Params: awgparams.Params{S4: 16}}
	rec := pushParamsReq(t, srv, "100.64.0.1", ParamPush{Pending: committed})

	var ack ParamAck
	if err := json.NewDecoder(rec.Body).Decode(&ack); err != nil {
		t.Fatalf("decode ack: %v", err)
	}
	if ack.Commit != 5 {
		t.Errorf("commit-ack = %d, want 5 (нода получила committed ApplyAt)", ack.Commit)
	}
	s, _ := store.Read()
	if s.Pending == nil || s.Pending.ApplyAt.IsZero() || !s.Pending.ApplyAt.Equal(applyAt) {
		t.Fatalf("committed ApplyAt не подхвачен: %+v", s.Pending)
	}
}

// Идемпотентность: повторный push того же Pending → ErrNoChange внутри, но ВСЁ РАВНО
// 200 с актуальным ack (seed обязан получить подтверждение и на повторе, иначе зависнет).
func TestHandleParams_Idempotent(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "SELF", NodeIP: "100.64.0.2",
		ParamsVersion: 4, Peers: seedPeers("100.64.0.1", "SELF", "100.64.0.2"),
	})
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())
	push := ParamPush{Pending: &state.PendingParams{Version: 5}}

	_ = pushParamsReq(t, srv, "100.64.0.1", push)
	rec := pushParamsReq(t, srv, "100.64.0.1", push) // повтор

	if rec.Code != http.StatusOK {
		t.Fatalf("повторный push: status %d, want 200", rec.Code)
	}
	var ack ParamAck
	if err := json.NewDecoder(rec.Body).Decode(&ack); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ack.Announce != 5 {
		t.Errorf("повторный ack.Announce = %d, want 5", ack.Announce)
	}
}

// Безопасность: push легитимен ТОЛЬКО от seed'а (Pending раздаёт seed). Запрос с
// mesh-IP не-seed-пира обязан отклоняться (403) и НЕ менять state. Иначе любой
// инсайдер изнутри туннеля навязал бы сети произвольный flag-day.
func TestHandleParams_RejectsNonSeed(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "SELF", NodeIP: "100.64.0.2",
		ParamsVersion: 4,
		Peers: []state.Peer{
			{Label: "seed", PublicKey: "SEEDKEY", NodeIP: "100.64.0.1", IsSeed: true},
			{Label: "self", PublicKey: "SELF", NodeIP: "100.64.0.2"},
			{Label: "evil", PublicKey: "EVIL", NodeIP: "100.64.0.9"}, // обычный пир
		},
	})
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

	rec := pushParamsReq(t, srv, "100.64.0.9", ParamPush{Pending: &state.PendingParams{Version: 5}})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("push не от seed: status %d, want 403", rec.Code)
	}
	s, _ := store.Read()
	if s.Pending != nil {
		t.Fatalf("неавторизованный push не должен менять state: %+v", s.Pending)
	}
}

// handleParams отвергает не-POST (405), nil-Pending (400) и битый JSON (400).
func TestHandleParams_RejectsBad(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "SELF", NodeIP: "100.64.0.2",
		Peers: seedPeers("100.64.0.1", "SELF", "100.64.0.2"),
	})
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handleParams(rec, httptest.NewRequest(http.MethodGet, "/v1/params", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d, want 405", rec.Code)
	}

	if rec := pushParamsReq(t, srv, "100.64.0.1", ParamPush{Pending: nil}); rec.Code != http.StatusBadRequest {
		t.Fatalf("nil Pending: status %d, want 400", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/params", strings.NewReader("{not json"))
	req.RemoteAddr = "100.64.0.1:40000"
	rec = httptest.NewRecorder()
	srv.handleParams(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage body: status %d, want 400", rec.Code)
	}
}

// Гигантское тело POST не должно прочитываться целиком (DoS на память роутера).
func TestHandleParams_BodyTooLarge(t *testing.T) {
	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "SELF", NodeIP: "100.64.0.2",
		Peers: seedPeers("100.64.0.1", "SELF", "100.64.0.2"),
	})
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

	// Раздуваем тело мусором сверх лимита через лишнее поле в JSON.
	huge := `{"pending":{"version":5},"junk":"` + strings.Repeat("A", maxParamBody+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/params", strings.NewReader(huge))
	req.RemoteAddr = "100.64.0.1:40000"
	rec := httptest.NewRecorder()
	srv.handleParams(rec, req)

	if rec.Code == http.StatusOK {
		t.Fatal("гигантское тело должно быть отвергнуто (MaxBytesReader)")
	}
}

// PushParams end-to-end: отправляет Pending, парсит ack из 200-ответа; не-200 → ошибка.
func TestPushParams_RoundTrip(t *testing.T) {
	var got ParamPush
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(ParamAck{Announce: 7, Commit: 6})
	}))
	defer ts.Close()
	host, portStr, _ := net.SplitHostPort(ts.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	hc := &http.Client{Timeout: 5 * time.Second}
	ack, err := PushParams(context.Background(), hc, host, port,
		ParamPush{Pending: &state.PendingParams{Version: 7}})
	if err != nil {
		t.Fatalf("PushParams: %v", err)
	}
	if ack.Announce != 7 || ack.Commit != 6 {
		t.Fatalf("ack = %+v, want {7,6}", ack)
	}
	if got.Pending == nil || got.Pending.Version != 7 {
		t.Fatalf("сервер получил не тот Pending: %+v", got.Pending)
	}

	// Сбойный узел (500) → ошибка, не паника.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	bh, bp, _ := net.SplitHostPort(bad.Listener.Addr().String())
	bpn, _ := strconv.Atoi(bp)
	if _, err := PushParams(context.Background(), hc, bh, bpn, ParamPush{Pending: &state.PendingParams{Version: 7}}); err == nil {
		t.Fatal("ожидалась ошибка на 500-ответ")
	}
}
