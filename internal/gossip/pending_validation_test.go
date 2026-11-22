package gossip

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams/awgtest"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
)

// Pending с набором, который device отвергнет, не должен попасть в state ни одним из
// двух каналов доставки (push от seed и gossip-pull). Иначе флиппер запишет его в
// state.AwgParams, и на следующем рестарте Configure не поднимет awg0 — нода потеряна
// (у части нод нет out-of-band). Без ack seed не закоммитит flag-day: mesh остаётся
// согласованным на старом наборе.

func nodeState(pending *state.PendingParams) *state.State {
	return &state.State{
		NetworkCIDR: "100.64.0.0/24", PublicKey: "SELF", NodeIP: "100.64.0.2",
		ParamsVersion: 4, AwgParams: awgtest.Valid(7), Pending: pending,
		Peers: seedPeers("100.64.0.1", "SELF", "100.64.0.2"),
	}
}

// --- push (POST /v1/params) ---

func TestHandleParams_RejectsDeviceInvalidAnnounce(t *testing.T) {
	for name, bad := range awgtest.DeviceInvalid(awgtest.Valid(16)) {
		t.Run(name, func(t *testing.T) {
			before := nodeState(nil)
			store := saveState(t, before)
			srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

			rec := pushParamsReq(t, srv, "100.64.0.1", ParamPush{Pending: &state.PendingParams{Version: 5, Params: bad}})

			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status %d, want 422 (seed должен увидеть отказ, а не ack)", rec.Code)
			}
			assertStateUnchanged(t, store, before)
		})
	}
}

// Commit поверх уже принятого валидного анонса той же версии, но с ДРУГИМ (кривым)
// набором — тоже отказ: commit несёт params, и флиппер применит именно их.
func TestHandleParams_RejectsDeviceInvalidCommitOverAnnounce(t *testing.T) {
	applyAt := time.Now().Add(time.Minute).UTC()
	for name, bad := range awgtest.DeviceInvalid(awgtest.Valid(16)) {
		t.Run(name, func(t *testing.T) {
			before := nodeState(&state.PendingParams{Version: 5, Params: awgtest.Valid(16)})
			store := saveState(t, before)
			srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

			rec := pushParamsReq(t, srv, "100.64.0.1", ParamPush{Pending: &state.PendingParams{Version: 5, ApplyAt: applyAt, Params: bad}})

			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status %d, want 422", rec.Code)
			}
			assertStateUnchanged(t, store, before)
		})
	}
}

func TestHandleParams_AcceptsOutsidePolicyDeviceValid(t *testing.T) {
	for name, p := range awgtest.OutsidePolicyDeviceValid(awgtest.Valid(16)) {
		t.Run(name, func(t *testing.T) {
			store := saveState(t, nodeState(nil))
			srv := NewServer("100.64.0.2", DefaultPort, store, discardLogger())

			rec := pushParamsReq(t, srv, "100.64.0.1", ParamPush{Pending: &state.PendingParams{Version: 5, Params: p}})

			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, want 200 (не строже device)", rec.Code)
			}
			var ack ParamAck
			if err := json.NewDecoder(rec.Body).Decode(&ack); err != nil || ack.Announce != 5 {
				t.Fatalf("ack=%+v err=%v, want announce 5", ack, err)
			}
			if s, _ := store.Read(); s.Pending == nil || s.Pending.Params != p {
				t.Fatalf("набор не принят: %+v", s.Pending)
			}
		})
	}
}

// --- gossip-pull (doRound) ---

func pullFrom(t *testing.T, before *state.State, theirs *state.PendingParams) *state.Store {
	t.Helper()
	const self, targetKey = "SELF", "targetkey"
	host, port, ts := fakeServerWithPending(t,
		[]proto.PeerInfo{{Label: "t", PublicKey: targetKey, NodeIP: "100.64.0.9", Endpoint: "203.0.113.1:51820"}},
		4, theirs)
	t.Cleanup(ts.Close)
	before.Peers = []state.Peer{{Label: "target", PublicKey: targetKey, NodeIP: host, Endpoint: "203.0.113.1:51820"}}
	store := saveState(t, before)
	NewClient(store, self, time.Minute, port, nil, nil, discardLogger()).doRound(context.Background())
	return store
}

func TestDoRound_RejectsDeviceInvalidPending(t *testing.T) {
	applyAt := time.Now().Add(time.Minute).UTC()
	for name, bad := range awgtest.DeviceInvalid(awgtest.Valid(16)) {
		t.Run(name, func(t *testing.T) {
			store := pullFrom(t, nodeState(nil), &state.PendingParams{Version: 5, ApplyAt: applyAt, Params: bad})
			if s, _ := store.Read(); s.Pending != nil {
				t.Fatalf("кривой Pending принят из gossip: %+v", s.Pending)
			}
		})
	}
}

func TestDoRound_RejectsDeviceInvalidCommitOverAnnounce(t *testing.T) {
	applyAt := time.Now().Add(time.Minute).UTC()
	for name, bad := range awgtest.DeviceInvalid(awgtest.Valid(16)) {
		t.Run(name, func(t *testing.T) {
			local := &state.PendingParams{Version: 5, Params: awgtest.Valid(16)}
			store := pullFrom(t, nodeState(local), &state.PendingParams{Version: 5, ApplyAt: applyAt, Params: bad})
			s, _ := store.Read()
			if s.Pending == nil || s.Pending.Params != awgtest.Valid(16) || !s.Pending.ApplyAt.IsZero() {
				t.Fatalf("локальный анонс подменён кривым commit'ом: %+v", s.Pending)
			}
		})
	}
}

func TestDoRound_AcceptsOutsidePolicyDeviceValid(t *testing.T) {
	applyAt := time.Now().Add(time.Minute).UTC()
	for name, p := range awgtest.OutsidePolicyDeviceValid(awgtest.Valid(16)) {
		t.Run(name, func(t *testing.T) {
			store := pullFrom(t, nodeState(nil), &state.PendingParams{Version: 5, ApplyAt: applyAt, Params: p})
			if s, _ := store.Read(); s.Pending == nil || s.Pending.Params != p {
				t.Fatalf("набор не принят (строже device): %+v", s.Pending)
			}
		})
	}
}

// assertStateUnchanged — state на диске совпадает с исходным (кроме служебного, что
// мог тронуть Save: сравниваем то, что важно для старта и flag-day).
func assertStateUnchanged(t *testing.T, store *state.Store, before *state.State) {
	t.Helper()
	s, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if s.ParamsVersion != before.ParamsVersion || s.AwgParams != before.AwgParams ||
		!reflect.DeepEqual(s.Pending, before.Pending) {
		t.Fatalf("state изменён отвергнутым push'ем:\n got  v=%d params=%+v pending=%+v\n want v=%d params=%+v pending=%+v",
			s.ParamsVersion, s.AwgParams, s.Pending, before.ParamsVersion, before.AwgParams, before.Pending)
	}
}
