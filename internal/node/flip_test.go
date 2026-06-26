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

// flipperFor — paramFlipper, который УЖЕ наблюдал committed ApplyAt версии seenVer с
// момента seenAt (имитируем «увидели коммит вовремя»). Нулевой seenVer = ничего не
// наблюдали. abortMargin/maxStale задаются явно.
func flipperFor(store *state.Store, dev Device, abortMargin, maxStale time.Duration, seenVer uint64, seenAt time.Time) *paramFlipper {
	return &paramFlipper{
		store: store, dev: dev, abortMargin: abortMargin, maxStale: maxStale, log: quietLogger(),
		seenVer: seenVer, seenAt: seenAt,
	}
}

func TestApplyIfDue(t *testing.T) {
	newParams := awgparams.Params{S4: 16, Jc: 5} // «новый» набор, отличим от старого
	now := time.Now().UTC()

	t.Run("due и держим коммит давно — применяется и в state, и в device", func(t *testing.T) {
		applyAt := now.Add(-time.Minute)
		store := storeWithPending(t, &state.PendingParams{Params: newParams, Version: 2, ApplyAt: applyAt})
		dev := &fakeDevice{}
		// наблюдали коммит за час до ApplyAt → гард held пройден
		f := flipperFor(store, dev, time.Minute, time.Hour, 2, applyAt.Add(-time.Hour))
		f.applyIfDue(now)

		if !dev.appliedParams {
			t.Error("device.ApplyParams не вызван")
		}
		s, _ := store.Read()
		if s.ParamsVersion != 2 || s.AwgParams != newParams || s.Pending != nil {
			t.Errorf("state не применён: version=%d params=%+v pending=%v", s.ParamsVersion, s.AwgParams, s.Pending)
		}
	})

	t.Run("ApplyAt в будущем — no-op", func(t *testing.T) {
		applyAt := now.Add(time.Hour)
		store := storeWithPending(t, &state.PendingParams{Params: newParams, Version: 2, ApplyAt: applyAt})
		dev := &fakeDevice{}
		f := flipperFor(store, dev, time.Minute, time.Hour, 2, now)
		f.applyIfDue(now)

		if dev.appliedParams {
			t.Error("device.ApplyParams вызван раньше ApplyAt")
		}
		s, _ := store.Read()
		if s.ParamsVersion != 1 || s.Pending == nil {
			t.Errorf("state изменён до срока: version=%d pending=%v", s.ParamsVersion, s.Pending)
		}
	})

	t.Run("Pending отсутствует — no-op", func(t *testing.T) {
		store := storeWithPending(t, nil)
		dev := &fakeDevice{}
		f := flipperFor(store, dev, time.Minute, time.Hour, 0, time.Time{})
		f.applyIfDue(now)

		if dev.appliedParams {
			t.Error("device.ApplyParams вызван без Pending")
		}
	})
}

// АНТИ-SPLIT: tick через observe запоминает момент первого наблюдения committed ApplyAt;
// флипает, только если держит его не меньше abortMargin. Коммит, увиденный впритык к
// ApplyAt, НЕ применяется — возможен abort в полёте (ровно сценарий, изолировавший ноду).
func TestParamFlipper_GuardOnLateCommit(t *testing.T) {
	newParams := awgparams.Params{S4: 16}
	t0 := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	applyAt := t0.Add(4 * time.Minute)

	t.Run("коммит увиден вовремя (>= abortMargin до ApplyAt) — флип", func(t *testing.T) {
		store := storeWithPending(t, &state.PendingParams{Params: newParams, Version: 2, ApplyAt: applyAt})
		dev := &fakeDevice{}
		f := flipperFor(store, dev, time.Minute, 10*time.Minute, 0, time.Time{})

		f.tick(t0)                       // впервые видим committed v2 в t0 (за 4мин до ApplyAt)
		if dev.appliedParams {
			t.Fatal("не должен флипать до ApplyAt")
		}
		f.tick(applyAt.Add(time.Second)) // ApplyAt наступил, держим коммит с t0 (>1мин) → флип
		if !dev.appliedParams {
			t.Fatal("должен флипнуть: committed ApplyAt держится дольше abortMargin")
		}
	})

	t.Run("коммит увиден впритык (< abortMargin до ApplyAt) — НЕ флип", func(t *testing.T) {
		store := storeWithPending(t, &state.PendingParams{Params: newParams, Version: 2, ApplyAt: applyAt})
		dev := &fakeDevice{}
		f := flipperFor(store, dev, time.Minute, 10*time.Minute, 0, time.Time{})

		f.tick(applyAt.Add(-30 * time.Second)) // впервые видим коммит за 30с (< 1мин) до ApplyAt
		f.tick(applyAt.Add(time.Second))       // ApplyAt наступил, но держим < abortMargin
		if dev.appliedParams {
			t.Fatal("коммит получен впритык — флипать нельзя (возможен abort в полёте)")
		}
	})
}

// observe сбрасывает наблюдение, когда Pending уходит в анонс (abort на v+1): следующий
// committed обязан переснять seenAt, иначе старое наблюдение «протащило» бы новый flip.
func TestParamFlipper_ObserveResetsOnAnnounce(t *testing.T) {
	t0 := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	f := &paramFlipper{abortMargin: time.Minute, maxStale: time.Hour, log: quietLogger()}

	f.observe(&state.PendingParams{Version: 2, ApplyAt: t0}, t0) // committed v2
	if f.seenVer != 2 || !f.seenAt.Equal(t0) {
		t.Fatalf("после committed: seenVer=%d seenAt=%v", f.seenVer, f.seenAt)
	}
	f.observe(&state.PendingParams{Version: 3}, t0.Add(time.Minute)) // abort → announced v3
	if f.seenVer != 0 {
		t.Fatalf("анонс должен сбросить seenVer, got %d", f.seenVer)
	}
	later := t0.Add(2 * time.Minute)
	f.observe(&state.PendingParams{Version: 3, ApplyAt: later}, later) // committed v3
	if f.seenVer != 3 || !f.seenAt.Equal(later) {
		t.Fatalf("committed v3 должен переснять seenAt: seenVer=%d seenAt=%v", f.seenVer, f.seenAt)
	}
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
		commitIfAllAcked(store, map[string]uint64{"A": 2, "B": 2}, "SELF", 30*time.Second, quietLogger())
		s, _ := store.Read()
		if s.Pending == nil || s.Pending.ApplyAt.IsZero() {
			t.Fatalf("должен закоммититься: %+v", s.Pending)
		}
	})

	t.Run("НЕ все подтвердили — НЕ коммитим (гарантия: ноду не теряем)", func(t *testing.T) {
		store := seedStore(t, announced())
		commitIfAllAcked(store, map[string]uint64{"A": 2}, "SELF", 30*time.Second, quietLogger()) // B молчит
		s, _ := store.Read()
		if s.Pending == nil || !s.Pending.ApplyAt.IsZero() {
			t.Fatalf("не должен коммититься пока B не подтвердил: %+v", s.Pending)
		}
	})

	t.Run("уже закоммичен — no-op", func(t *testing.T) {
		committed := &state.PendingParams{Version: 2, ApplyAt: time.Now().Add(time.Minute).UTC()}
		store := seedStore(t, committed)
		commitIfAllAcked(store, map[string]uint64{"A": 2, "B": 2}, "SELF", 30*time.Second, quietLogger())
		s, _ := store.Read()
		if !s.Pending.ApplyAt.Equal(committed.ApplyAt) {
			t.Error("ApplyAt уже назначенного Pending не должен меняться")
		}
	})

	t.Run("нет Pending — no-op", func(t *testing.T) {
		store := seedStore(t, nil)
		commitIfAllAcked(store, map[string]uint64{"A": 2, "B": 2}, "SELF", 30*time.Second, quietLogger())
		s, _ := store.Read()
		if s.Pending != nil {
			t.Error("Pending не должен появиться из ниоткуда")
		}
	})
}

// committedAt — Pending версии v, закоммиченный на applyAt.
func committedAt(v uint64, applyAt time.Time) *state.PendingParams {
	return &state.PendingParams{Params: awgparams.Params{S4: 16}, Version: v, ApplyAt: applyAt}
}

// abortIfStuck отменяет flip (переанонс v+1, ApplyAt=0), если к дедлайну не все
// подтвердили приём committed ApplyAt — иначе медленная нода застрянет на старом наборе.
func TestAbortIfStuck(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	const margin = time.Minute

	t.Run("не все commit-acked + дедлайн настал → abort (переанонс v+1)", func(t *testing.T) {
		store := seedStore(t, committedAt(2, now.Add(30*time.Second))) // ApplyAt-margin = now-30с ≤ now
		abortIfStuck(store, map[string]uint64{"A": 2}, "SELF", margin, now, quietLogger())          // B молчит
		s, _ := store.Read()
		if s.Pending == nil || s.Pending.Version != 3 || !s.Pending.ApplyAt.IsZero() {
			t.Fatalf("должен переанонсить v3 announced: %+v", s.Pending)
		}
	})

	t.Run("все commit-acked (armed) → НЕ abort", func(t *testing.T) {
		store := seedStore(t, committedAt(2, now.Add(30*time.Second)))
		abortIfStuck(store, map[string]uint64{"A": 2, "B": 2}, "SELF", margin, now, quietLogger())
		s, _ := store.Read()
		if s.Pending.Version != 2 || s.Pending.ApplyAt.IsZero() {
			t.Fatalf("armed flip не должен абортиться: %+v", s.Pending)
		}
	})

	t.Run("дедлайн ещё не настал → НЕ abort", func(t *testing.T) {
		store := seedStore(t, committedAt(2, now.Add(10*time.Minute))) // ApplyAt-margin = now+9мин > now
		abortIfStuck(store, map[string]uint64{"A": 2}, "SELF", margin, now, quietLogger())
		s, _ := store.Read()
		if s.Pending.Version != 2 || s.Pending.ApplyAt.IsZero() {
			t.Fatalf("до дедлайна не абортим: %+v", s.Pending)
		}
	})

	t.Run("announced (нет ApplyAt) → нечего абортить", func(t *testing.T) {
		store := seedStore(t, announced())
		abortIfStuck(store, map[string]uint64{}, "SELF", margin, now, quietLogger())
		s, _ := store.Read()
		if s.Pending.Version != 2 || !s.Pending.ApplyAt.IsZero() {
			t.Fatalf("announced Pending abort не трогает: %+v", s.Pending)
		}
	})
}

// bothAcker — провайдер ОБОИХ ack-каналов для runParamCommit (вместо gossip-сервера).
type bothAcker struct {
	ack    func() map[string]uint64
	commit func() map[string]uint64
}

func (b bothAcker) Acks() map[string]uint64       { return b.ack() }
func (b bothAcker) CommitAcks() map[string]uint64 { return b.commit() }

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

// applyIfDue не падает, если device отверг новые params; state при этом уже применён
// (Update прошёл до вызова device — рехендшейк подтянет позже).
func TestApplyIfDueDeviceError(t *testing.T) {
	now := time.Now().UTC()
	applyAt := now.Add(-time.Minute)
	store := storeWithPending(t, &state.PendingParams{Version: 2, ApplyAt: applyAt})
	dev := &fakeDevice{applyParamsErr: errors.New("ipcset boom")}
	f := flipperFor(store, dev, time.Minute, time.Hour, 2, applyAt.Add(-time.Hour))

	f.applyIfDue(now) // не должен паниковать

	s, _ := store.Read()
	if s.ParamsVersion != 2 || s.Pending != nil {
		t.Errorf("state должен примениться даже при ошибке device: version=%d pending=%v", s.ParamsVersion, s.Pending)
	}
}

// runParamFlip (ticker) применяет due-Pending (с пройденным гардом held) и завершается по ctx.
func TestRunParamFlip(t *testing.T) {
	applyAt := time.Now().Add(-time.Second).UTC()
	store := storeWithPending(t, &state.PendingParams{Version: 2, ApplyAt: applyAt})
	// наблюдали коммит задолго до ApplyAt → гард пройден
	f := flipperFor(store, &fakeDevice{}, 10*time.Millisecond, time.Hour, 2, applyAt.Add(-time.Hour))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runParamFlip(ctx, f, 5*time.Millisecond)

	waitFor(t, func() bool { s, _ := store.Read(); return s.Pending == nil })
}

// runParamCommit happy-path: все подтвердили И анонс, И приём ApplyAt → коммитит и НЕ
// абортит (armed), ApplyAt держится.
func TestRunParamCommit_CommitsWhenAllAcked(t *testing.T) {
	store := seedStore(t, announced())
	all := func() map[string]uint64 { return map[string]uint64{"A": 2, "B": 2} }
	acker := bothAcker{ack: all, commit: all}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// grace велик, abortMargin велик → abort в коротком тесте не сработает; armed и так держит
	go runParamCommit(ctx, store, acker, "SELF", 5*time.Millisecond, 30*time.Second, 10*time.Second, quietLogger())

	waitFor(t, func() bool { s, _ := store.Read(); return s.Pending != nil && !s.Pending.ApplyAt.IsZero() })
}

// РЕГРЕСС flint2: анонс подтвердили все, но приём committed ApplyAt — НЕ все (B молчит).
// seed обязан НЕ оставить committed flip, а отменить его (переанонс v>2). Так seed не
// уходит на новый набор в одиночку, изолируя B.
func TestRunParamCommit_AbortsWhenCommitAcksMissing(t *testing.T) {
	store := seedStore(t, announced())
	acker := bothAcker{
		ack:    func() map[string]uint64 { return map[string]uint64{"A": 2, "B": 2} }, // анонс — все
		commit: func() map[string]uint64 { return map[string]uint64{"A": 2} },         // приём ApplyAt — только A
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// grace мал, abortMargin мал → быстро коммитит v2 и быстро абортит в v3
	go runParamCommit(ctx, store, acker, "SELF", 2*time.Millisecond, 12*time.Millisecond, 4*time.Millisecond, quietLogger())

	waitFor(t, func() bool {
		s, _ := store.Read()
		return s.Pending != nil && s.Pending.Version > 2 && s.Pending.ApplyAt.IsZero()
	})
}

// commitGraceFor / abortMarginFor привязывают окна к gossip-интервалу. Ключевой инвариант
// против регресса: commitGrace > abortMargin (нужен зазор собрать commit-ack'и до дедлайна).
func TestCommitGraceAndAbortMargin(t *testing.T) {
	if got := commitGraceFor(time.Minute); got != commitGraceCycles*time.Minute {
		t.Errorf("commitGraceFor(60s) = %v, want %v", got, commitGraceCycles*time.Minute)
	}
	if got := commitGraceFor(time.Second); got != 30*time.Second {
		t.Errorf("commitGraceFor(1s) = %v, want 30s (нижняя граница)", got)
	}
	if got := abortMarginFor(time.Minute); got != abortMarginCycles*time.Minute {
		t.Errorf("abortMarginFor(60s) = %v, want %v", got, abortMarginCycles*time.Minute)
	}
	if got := abortMarginFor(time.Second); got != 15*time.Second {
		t.Errorf("abortMarginFor(1s) = %v, want 15s (нижняя граница)", got)
	}
	// Инвариант на больших и малых интервалах.
	for _, g := range []time.Duration{0, time.Second, 10 * time.Second, time.Minute, 5 * time.Minute} {
		if commitGraceFor(g) <= abortMarginFor(g) {
			t.Fatalf("commitGrace(%v)=%v ОБЯЗАН быть > abortMargin=%v", g, commitGraceFor(g), abortMarginFor(g))
		}
	}
}

// Регресс на инцидент: «бродячий» committed Pending с давно прошедшим ApplyAt НЕ должен
// применяться (maxStale), даже если held-гард формально пройден.
func TestApplyIfDueRejectsStalePending(t *testing.T) {
	now := time.Now().UTC()
	applyAt := now.Add(-time.Hour) // ApplyAt час назад — протух
	store := storeWithPending(t, &state.PendingParams{Params: awgparams.Params{S4: 16}, Version: 2, ApplyAt: applyAt})
	dev := &fakeDevice{}
	// держим коммит давно (гард held пройден), но maxStale=1мин ≪ 1час
	f := flipperFor(store, dev, time.Second, time.Minute, 2, applyAt.Add(-time.Hour))

	f.applyIfDue(now)

	if dev.appliedParams {
		t.Fatal("протухший Pending (ApplyAt час назад) НЕ должен применяться")
	}
	s, _ := store.Read()
	if s.ParamsVersion == 2 {
		t.Fatal("бродячий Pending не должен менять state")
	}
}
