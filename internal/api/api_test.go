package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
)

// --- helpers ---

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// sampleState — seed с одним пиром, детерминированные значения для golden-проверок.
func sampleState() *state.State {
	return &state.State{
		NodeLabel:     "seed",
		NetworkCIDR:   "100.64.0.0/10",
		NodeIP:        "100.64.0.1",
		PublicKey:     "PUBKEYSEED",
		ListenPort:    51820,
		IsSeed:        true,
		ParamsVersion: 3,
		Peers: []state.Peer{
			{Label: "seed", PublicKey: "PUBKEYSEED", NodeIP: "100.64.0.1", Endpoint: "203.0.113.10:51820", IsSeed: true},
			{Label: "gw-1", PublicKey: "PUBKEYGW", NodeIP: "100.64.0.2", Endpoint: "203.0.113.24:51820"},
		},
		UpdatedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
}

func testStore(t *testing.T, s *state.State) *state.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := s.Save(path); err != nil {
		t.Fatalf("save state: %v", err)
	}
	return state.NewStore(path)
}

func testHandler(t *testing.T, s *state.State) http.Handler {
	t.Helper()
	return NewServer("127.0.0.1", DefaultPort, testStore(t, s), nil, discardLogger()).Handler()
}

func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
}

type errBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// --- read-эндпоинты ---

func TestStatusEndpoint(t *testing.T) {
	rec := do(t, testHandler(t, sampleState()), http.MethodGet, "/api/v1/status")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	var body struct {
		Data struct {
			NodeIP string `json:"node_ip"`
			IsSeed bool   `json:"is_seed"`
			Role   string `json:"role"`
			Peers  []struct {
				Label  string `json:"label"`
				IsSelf bool   `json:"is_self"`
			} `json:"peers"`
		} `json:"data"`
	}
	decode(t, rec, &body)

	if body.Data.NodeIP != "100.64.0.1" {
		t.Errorf("node_ip = %q, want 100.64.0.1", body.Data.NodeIP)
	}
	if !body.Data.IsSeed {
		t.Error("is_seed = false, want true")
	}
	if body.Data.Role != "seed" {
		t.Errorf("role = %q, want seed", body.Data.Role)
	}
	if len(body.Data.Peers) != 2 {
		t.Fatalf("peers = %d, want 2", len(body.Data.Peers))
	}
	// собственная запись должна быть помечена is_self.
	var selfMarked bool
	for _, p := range body.Data.Peers {
		if p.Label == "seed" && p.IsSelf {
			selfMarked = true
		}
	}
	if !selfMarked {
		t.Error("собственный peer не помечен is_self")
	}
}

func TestPeersEndpoint(t *testing.T) {
	rec := do(t, testHandler(t, sampleState()), http.MethodGet, "/api/v1/peers")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data []struct {
			Label  string `json:"label"`
			NodeIP string `json:"node_ip"`
		} `json:"data"`
	}
	decode(t, rec, &body)
	if len(body.Data) != 2 {
		t.Fatalf("data = %d пиров, want 2", len(body.Data))
	}
}

// Пустой peer-list обязан быть JSON-массивом [], а не null — иначе фронтенд,
// делающий .map/.length, падает. BuildStatus отдаёт non-nil slice.
func TestPeersEmptyStateReturnsArrayNotNull(t *testing.T) {
	s := sampleState()
	s.Peers = nil
	rec := do(t, testHandler(t, s), http.MethodGet, "/api/v1/peers")

	var raw map[string]json.RawMessage
	decode(t, rec, &raw)
	if got := string(raw["data"]); got != "[]" {
		t.Errorf("data = %s, want []", got)
	}
}

func TestHealthEndpoint(t *testing.T) {
	rec := do(t, testHandler(t, sampleState()), http.MethodGet, "/api/v1/health")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Data struct {
			IsSeed         bool   `json:"is_seed"`
			ParamsVersion  uint64 `json:"params_version"`
			PeersTotal     int    `json:"peers_total"`
			PendingFlagDay bool   `json:"pending_flag_day"`
		} `json:"data"`
	}
	decode(t, rec, &body)

	if !body.Data.IsSeed {
		t.Error("is_seed = false, want true")
	}
	if body.Data.ParamsVersion != 3 {
		t.Errorf("params_version = %d, want 3", body.Data.ParamsVersion)
	}
	if body.Data.PeersTotal != 2 {
		t.Errorf("peers_total = %d, want 2", body.Data.PeersTotal)
	}
	if body.Data.PendingFlagDay {
		t.Error("pending_flag_day = true, want false (Pending не задан)")
	}
}

// --- live-обогащение ---

// Свежий handshake пира → live_status "online" на /status.
func TestStatusEndpointLiveOnline(t *testing.T) {
	stats := func() (map[string]mesh.PeerLive, error) {
		return map[string]mesh.PeerLive{
			"PUBKEYGW": {LastHandshake: time.Now().Add(-10 * time.Second)},
		}, nil
	}
	h := NewServer("127.0.0.1", DefaultPort, testStore(t, sampleState()), stats, discardLogger()).Handler()
	rec := do(t, h, http.MethodGet, "/api/v1/status")

	var body struct {
		Data struct {
			Peers []struct {
				PublicKey  string `json:"public_key"`
				LiveStatus string `json:"live_status"`
			} `json:"peers"`
		} `json:"data"`
	}
	decode(t, rec, &body)

	var found bool
	for _, p := range body.Data.Peers {
		if p.PublicKey == "PUBKEYGW" {
			found = true
			if p.LiveStatus != "online" {
				t.Errorf("gw live_status = %q, want online", p.LiveStatus)
			}
		}
	}
	if !found {
		t.Fatal("пир PUBKEYGW не найден в ответе")
	}
}

// Ошибка источника live-статистики (device недоступен) → 200 со state-only,
// без live_status; НЕ 500 — live-обогащение необязательно.
func TestStatusEndpointStatsErrorDegradesToStateOnly(t *testing.T) {
	stats := func() (map[string]mesh.PeerLive, error) {
		return nil, errors.New("device unavailable")
	}
	h := NewServer("127.0.0.1", DefaultPort, testStore(t, sampleState()), stats, discardLogger()).Handler()
	rec := do(t, h, http.MethodGet, "/api/v1/status")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (деградация, не 500)", rec.Code)
	}
	var body struct {
		Data struct {
			Peers []struct {
				LiveStatus string `json:"live_status"`
			} `json:"peers"`
		} `json:"data"`
	}
	decode(t, rec, &body)
	for _, p := range body.Data.Peers {
		if p.LiveStatus != "" {
			t.Errorf("live_status = %q при ошибке источника, want пусто", p.LiveStatus)
		}
	}
}

// --- каноны ошибок ---

func TestUnknownRouteReturns404JSON(t *testing.T) {
	rec := do(t, testHandler(t, sampleState()), http.MethodGet, "/api/v1/nope")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	var e errBody
	decode(t, rec, &e)
	if e.Error.Code != "not_found" {
		t.Errorf("error.code = %q, want not_found", e.Error.Code)
	}
}

func TestMethodNotAllowedReturns405WithAllow(t *testing.T) {
	rec := do(t, testHandler(t, sampleState()), http.MethodPost, "/api/v1/status")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != "GET" {
		t.Errorf("Allow = %q, want GET", allow)
	}
	var e errBody
	decode(t, rec, &e)
	if e.Error.Code != "method_not_allowed" {
		t.Errorf("error.code = %q, want method_not_allowed", e.Error.Code)
	}
}

// Ошибка чтения state (файл отсутствует) → 500 с generic-сообщением, без утечки
// внутренней причины в тело.
func TestStoreErrorReturns500(t *testing.T) {
	badStore := state.NewStore(filepath.Join(t.TempDir(), "absent.json"))
	h := NewServer("127.0.0.1", DefaultPort, badStore, nil, discardLogger()).Handler()

	rec := do(t, h, http.MethodGet, "/api/v1/status")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var e errBody
	decode(t, rec, &e)
	if e.Error.Code != "internal" {
		t.Errorf("error.code = %q, want internal", e.Error.Code)
	}
	if e.Error.Message == "" || len(e.Error.Message) > 40 {
		t.Errorf("message %q не выглядит generic-текстом (утечка причины?)", e.Error.Message)
	}
}

// Паника в хендлере не должна ронять сервер — recover-middleware отдаёт 500.
func TestRecoverMiddlewareTurnsPanicInto500(t *testing.T) {
	srv := NewServer("127.0.0.1", DefaultPort, testStore(t, sampleState()), nil, discardLogger())
	h := srv.recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var e errBody
	decode(t, rec, &e)
	if e.Error.Code != "internal" {
		t.Errorf("error.code = %q, want internal", e.Error.Code)
	}
}
