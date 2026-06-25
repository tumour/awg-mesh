package node

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// storeWithPending — Store на temp-файле с текущими params версии 1 и заданным Pending.
func storeWithPending(t *testing.T, pending *state.PendingParams) *state.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s := &state.State{
		NodeLabel:     "n",
		ParamsVersion: 1,
		AwgParams:     awgparams.Params{S4: 0}, // «старый» набор
		Pending:       pending,
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	return state.NewStore(path)
}

func TestApplyDueParams(t *testing.T) {
	newParams := awgparams.Params{S4: 16, Jc: 5} // «новый» набор, отличим от старого

	t.Run("pending due — применяется и в state, и в device", func(t *testing.T) {
		store := storeWithPending(t, &state.PendingParams{
			Params: newParams, Version: 2, ApplyAt: time.Now().Add(-time.Minute).UTC(),
		})
		dev := &fakeDevice{}
		applyDueParams(store, dev, quietLogger())

		if !dev.appliedParams {
			t.Error("device.ApplyParams не вызван")
		}
		s, _ := store.Read()
		if s.ParamsVersion != 2 || s.AwgParams != newParams || s.Pending != nil {
			t.Errorf("state не применён: version=%d params=%+v pending=%v", s.ParamsVersion, s.AwgParams, s.Pending)
		}
	})

	t.Run("pending в будущем — no-op", func(t *testing.T) {
		store := storeWithPending(t, &state.PendingParams{
			Params: newParams, Version: 2, ApplyAt: time.Now().Add(time.Hour).UTC(),
		})
		dev := &fakeDevice{}
		applyDueParams(store, dev, quietLogger())

		if dev.appliedParams {
			t.Error("device.ApplyParams вызван раньше ApplyAt")
		}
		s, _ := store.Read()
		if s.ParamsVersion != 1 || s.Pending == nil {
			t.Errorf("state изменён до срока: version=%d pending=%v", s.ParamsVersion, s.Pending)
		}
	})

	t.Run("pending отсутствует — no-op", func(t *testing.T) {
		store := storeWithPending(t, nil)
		dev := &fakeDevice{}
		applyDueParams(store, dev, quietLogger())

		if dev.appliedParams {
			t.Error("device.ApplyParams вызван без Pending")
		}
	})
}

// seedStore — Store seed'а с двумя пирами (A, B) и заданным Pending.
func seedStore(t *testing.T, pending *state.PendingParams) *state.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s := &state.State{
		NodeLabel: "seed", PublicKey: "SELF", IsSeed: true, ParamsVersion: 1,
		Peers: []state.Peer{
			{PublicKey: "SELF", NodeIP: "100.64.0.1"},
			{PublicKey: "A", NodeIP: "100.64.0.2"},
			{PublicKey: "B", NodeIP: "100.64.0.3"},
		},
		Pending: pending,
	}
	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	return state.NewStore(path)
}

func announced() *state.PendingParams { return &state.PendingParams{Version: 2} } // ApplyAt нулевой

func TestCommitIfAllAcked(t *testing.T) {
	t.Run("все подтвердили — ApplyAt назначен", func(t *testing.T) {
		store := seedStore(t, announced())
		commitIfAllAcked(store, map[string]uint64{"A": 2, "B": 2}, "SELF", quietLogger())
		s, _ := store.Read()
		if s.Pending == nil || s.Pending.ApplyAt.IsZero() {
			t.Fatalf("должен закоммититься: %+v", s.Pending)
		}
	})

	t.Run("НЕ все подтвердили — НЕ коммитим (гарантия: ноду не теряем)", func(t *testing.T) {
		store := seedStore(t, announced())
		commitIfAllAcked(store, map[string]uint64{"A": 2}, "SELF", quietLogger()) // B молчит
		s, _ := store.Read()
		if s.Pending == nil || !s.Pending.ApplyAt.IsZero() {
			t.Fatalf("не должен коммититься пока B не подтвердил: %+v", s.Pending)
		}
	})

	t.Run("уже закоммичен — no-op", func(t *testing.T) {
		committed := &state.PendingParams{Version: 2, ApplyAt: time.Now().Add(time.Minute).UTC()}
		store := seedStore(t, committed)
		commitIfAllAcked(store, map[string]uint64{"A": 2, "B": 2}, "SELF", quietLogger())
		s, _ := store.Read()
		if !s.Pending.ApplyAt.Equal(committed.ApplyAt) {
			t.Error("ApplyAt уже назначенного Pending не должен меняться")
		}
	})

	t.Run("нет Pending — no-op", func(t *testing.T) {
		store := seedStore(t, nil)
		commitIfAllAcked(store, map[string]uint64{"A": 2, "B": 2}, "SELF", quietLogger())
		s, _ := store.Read()
		if s.Pending != nil {
			t.Error("Pending не должен появиться из ниоткуда")
		}
	})
}

// ackerFunc — провайдер ack'ов для runParamCommit (вместо gossip-сервера).
type ackerFunc func() map[string]uint64

func (f ackerFunc) Acks() map[string]uint64 { return f() }

// waitFor поллит cond до 2с (через store — без гонки на полях fake-device).
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("условие не выполнилось за 2с")
}

// applyDueParams не падает, если device отверг новые params; state при этом уже
// применён (Update прошёл до вызова device — рехендшейк подтянет позже).
func TestApplyDueParamsDeviceError(t *testing.T) {
	store := storeWithPending(t, &state.PendingParams{
		Version: 2, ApplyAt: time.Now().Add(-time.Minute).UTC(),
	})
	dev := &fakeDevice{applyParamsErr: errors.New("ipcset boom")}

	applyDueParams(store, dev, quietLogger()) // не должен паниковать

	s, _ := store.Read()
	if s.ParamsVersion != 2 || s.Pending != nil {
		t.Errorf("state должен примениться даже при ошибке device: version=%d pending=%v", s.ParamsVersion, s.Pending)
	}
}

// runParamFlip (ticker) применяет due-Pending и завершается по ctx.
func TestRunParamFlip(t *testing.T) {
	store := storeWithPending(t, &state.PendingParams{
		Version: 2, ApplyAt: time.Now().Add(-time.Second).UTC(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runParamFlip(ctx, store, &fakeDevice{}, 5*time.Millisecond, quietLogger())

	waitFor(t, func() bool { s, _ := store.Read(); return s.Pending == nil })
}

// runParamCommit (ticker, seed) назначает ApplyAt, когда все ack'нули.
func TestRunParamCommit(t *testing.T) {
	store := seedStore(t, announced())
	acker := ackerFunc(func() map[string]uint64 { return map[string]uint64{"A": 2, "B": 2} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runParamCommit(ctx, store, acker, "SELF", 5*time.Millisecond, quietLogger())

	waitFor(t, func() bool { s, _ := store.Read(); return s.Pending != nil && !s.Pending.ApplyAt.IsZero() })
}
