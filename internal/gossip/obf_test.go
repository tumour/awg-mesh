package gossip

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

// obfReq — POST /v1/obf с заданным источником (для проверки seed-авторизации).
func obfReq(t *testing.T, srcIP string, push ObfPush) *http.Request {
	t.Helper()
	body, err := json.Marshal(push)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/obf", bytes.NewReader(body))
	r.RemoteAddr = srcIP + ":40000"
	return r
}

// stateWithSeed — self на selfIP + один seed-пир на 100.64.0.1.
func stateWithSeed(selfIP string) *state.State {
	return &state.State{
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   "selfkey",
		NodeIP:      selfIP,
		Peers: []state.Peer{
			{Label: "seed", PublicKey: "seedkey", NodeIP: "100.64.0.1", IsSeed: true},
		},
	}
}

// Новая версия от seed → применяется (I1 в LocalObf, ObfVersion поднят), 204.
func TestHandleObf_AdoptsNewerFromSeed(t *testing.T) {
	store := saveState(t, stateWithSeed("100.64.0.2"))
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handleObf(rec, obfReq(t, "100.64.0.1", ObfPush{Version: 5, I1: "<b 0xdead>"}))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("code = %d, want 204", rec.Code)
	}
	st, err := store.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if st.ObfVersion != 5 || st.LocalObf.I1 != "<b 0xdead>" {
		t.Fatalf("obf not applied: version=%d i1=%q", st.ObfVersion, st.LocalObf.I1)
	}
}

// Та же или старее версия → no-op (идемпотентность), но всё равно 204 (ACK для seed-цикла).
func TestHandleObf_IdempotentSameOrOlder(t *testing.T) {
	st0 := stateWithSeed("100.64.0.2")
	st0.ObfVersion = 5
	st0.LocalObf.I1 = "<b 0xkeep>"
	store := saveState(t, st0)
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

	for _, v := range []uint64{5, 4} {
		rec := httptest.NewRecorder()
		srv.handleObf(rec, obfReq(t, "100.64.0.1", ObfPush{Version: v, I1: "<b 0xnew>"}))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("v=%d code = %d, want 204", v, rec.Code)
		}
	}
	st, _ := store.Read()
	if st.ObfVersion != 5 || st.LocalObf.I1 != "<b 0xkeep>" {
		t.Fatalf("idempotency broken: version=%d i1=%q", st.ObfVersion, st.LocalObf.I1)
	}
}

// Push НЕ от seed → 403, состояние не тронуто (чужой не может навязать obf).
func TestHandleObf_RejectsNonSeed(t *testing.T) {
	store := saveState(t, stateWithSeed("100.64.0.2"))
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handleObf(rec, obfReq(t, "100.64.0.3", ObfPush{Version: 5, I1: "<b 0xevil>"}))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403", rec.Code)
	}
	st, _ := store.Read()
	if st.ObfVersion != 0 {
		t.Fatalf("foreign obf-push applied! version=%d", st.ObfVersion)
	}
}

// Пустой I1 или нулевая версия → 400.
func TestHandleObf_RejectsBadBody(t *testing.T) {
	store := saveState(t, stateWithSeed("100.64.0.2"))
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

	for _, bad := range []ObfPush{{Version: 0, I1: "<b 0x>"}, {Version: 5, I1: ""}} {
		rec := httptest.NewRecorder()
		srv.handleObf(rec, obfReq(t, "100.64.0.1", bad))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%+v code = %d, want 400", bad, rec.Code)
		}
	}
}

// Не-POST → 405.
func TestHandleObf_RejectsWrongMethod(t *testing.T) {
	store := saveState(t, stateWithSeed("100.64.0.2"))
	srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

	rec := httptest.NewRecorder()
	srv.handleObf(rec, httptest.NewRequest(http.MethodGet, "/v1/obf", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("code = %d, want 405", rec.Code)
	}
}
