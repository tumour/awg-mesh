package mesh

import (
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
)

func pending(v uint64) *state.PendingParams { return &state.PendingParams{Version: v} }

// committed — Pending версии v с уже назначенным ApplyAt (момент применения).
func committed(v uint64) *state.PendingParams {
	return &state.PendingParams{Version: v, ApplyAt: time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)}
}

func TestShouldAdoptPending(t *testing.T) {
	cases := []struct {
		name       string
		curVersion uint64
		local      *state.PendingParams
		remote     *state.PendingParams
		want       bool
	}{
		{"remote nil — нечего принимать", 3, nil, nil, false},
		{"remote устарел (< current)", 5, nil, pending(4), false},
		{"remote == current — уже применён", 5, nil, pending(5), false},
		{"remote новее current, локального нет", 5, nil, pending(6), true},
		{"remote новее и current, и локального", 5, pending(6), pending(7), true},
		{"remote == локального pending (оба announced) — повтор, no-op", 5, pending(7), pending(7), false},
		{"remote старее локального pending", 5, pending(8), pending(7), false},

		// КЛЮЧЕВОЙ ФИКС: commit распространяется на ту же версию. Нода держит
		// announced-Pending (ApplyAt=0), seed прислал тот же Pending уже с ApplyAt —
		// без этого момент применения не доходит и флипает только seed (баг,
		// дважды клавший сеть).
		{"committed приходит на announced той же версии — ПРИНЯТЬ", 5, pending(6), committed(6), true},
		// Анти-reschedule: уже committed нельзя пере-планировать чужим committed.
		{"committed на committed (та же версия) — отвергнуть (анти-reschedule)", 5, committed(6), committed(6), false},
		// Не откатываемся с committed обратно на announced.
		{"announced на committed (та же версия) — отвергнуть (не назад)", 5, committed(6), pending(6), false},
		// committed уже применённой версии — устарело.
		{"committed версии <= current — устарело", 6, nil, committed(6), false},
		// Более новая версия побеждает независимо от commit-состояния локальной.
		{"announced новее версии, локально committed — принять (новее)", 5, committed(6), pending(7), true},
	}
	for _, c := range cases {
		if got := ShouldAdoptPending(c.curVersion, c.local, c.remote); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPendingDue(t *testing.T) {
	t0 := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	committed := &state.PendingParams{ApplyAt: t0}
	announced := &state.PendingParams{Version: 5} // ApplyAt нулевой — ещё не закоммичен
	const maxStale = time.Minute
	cases := []struct {
		name string
		p    *state.PendingParams
		now  time.Time
		want bool
	}{
		{"nil pending", nil, t0, false},
		{"announced (ApplyAt нулевой) — не применять", announced, t0, false},
		{"committed, до ApplyAt", committed, t0.Add(-time.Second), false},
		{"committed, ровно ApplyAt", committed, t0, true},
		{"committed, чуть после ApplyAt (в окне)", committed, t0.Add(time.Second), true},
		// Защита от «бродячего» Pending: ApplyAt давно прошёл (подхвачен по gossip
		// после отката) → НЕ применять, иначе мгновенный незапланированный flip.
		{"committed, ApplyAt протух (> maxStale назад)", committed, t0.Add(2 * time.Minute), false},
		{"committed, ровно граница maxStale — ещё валидно", committed, t0.Add(maxStale), true},
	}
	for _, c := range cases {
		if got := PendingDue(c.p, c.now, maxStale); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNewPending(t *testing.T) {
	p, _ := awgparams.Generate()

	// Нет висящего Pending → версия applied+1.
	pend := NewPending(7, nil, p)
	if pend.Version != 8 {
		t.Errorf("Version = %d, want 8", pend.Version)
	}
	if !pend.ApplyAt.IsZero() {
		t.Errorf("ApplyAt = %v, want нулевой (announced)", pend.ApplyAt)
	}
	if pend.Params != p {
		t.Error("Params не совпали")
	}
	if !ShouldAdoptPending(7, nil, pend) {
		t.Error("свежий announced Pending должен приниматься поверх current=7")
	}
	if PendingDue(pend, time.Now(), time.Minute) {
		t.Error("announced Pending не должен быть due (нет ApplyAt)")
	}

	// Повторный set-params ПОВЕРХ висящего Pending v8 обязан дать v9 (строго новее),
	// иначе его отвергнут как «не новее» и смена не разойдётся.
	over := NewPending(7, pending(8), p)
	if over.Version != 9 {
		t.Errorf("NewPending поверх Pending v8: Version = %d, want 9", over.Version)
	}
	if !ShouldAdoptPending(7, pending(8), over) {
		t.Error("анонс v9 должен приниматься поверх локального Pending v8")
	}
}

func TestAllPeersAcked(t *testing.T) {
	const self = "SELF"
	peers := []state.Peer{
		{PublicKey: self, NodeIP: "100.64.0.1"}, // мы — пропускается
		{PublicKey: "A", NodeIP: "100.64.0.2"},  // должен ack
		{PublicKey: "B", NodeIP: "100.64.0.3"},  // должен ack
		{PublicKey: "GHOST", NodeIP: ""},        // без mesh-IP — пропускается
	}
	cases := []struct {
		name string
		acks map[string]uint64
		want bool
	}{
		{"все ack'нули нужную версию", map[string]uint64{"A": 8, "B": 8}, true},
		{"ack новее (>= тоже ок)", map[string]uint64{"A": 9, "B": 8}, true},
		{"B не ack'нул", map[string]uint64{"A": 8}, false},
		{"B на старой версии", map[string]uint64{"A": 8, "B": 7}, false},
		{"пусто — никто не ack", map[string]uint64{}, false},
	}
	for _, c := range cases {
		if got := AllPeersAcked(peers, self, c.acks, 8); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
	// Сеть из одного seed (нет пиров) → коммитить можно сразу.
	if !AllPeersAcked([]state.Peer{{PublicKey: self, NodeIP: "100.64.0.1"}}, self, nil, 8) {
		t.Error("одинокий seed должен считаться all-acked")
	}
}

func TestCommitPending(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	if CommitPending(nil, now, time.Minute) {
		t.Error("nil pending — нечего коммитить")
	}

	p := &state.PendingParams{Version: 8} // announced
	if !CommitPending(p, now, 30*time.Second) {
		t.Fatal("announced Pending должен закоммититься")
	}
	if !p.ApplyAt.Equal(now.Add(30 * time.Second)) {
		t.Errorf("ApplyAt = %v, want %v", p.ApplyAt, now.Add(30*time.Second))
	}
	// Повторный commit не сдвигает уже назначенный ApplyAt.
	if CommitPending(p, now.Add(time.Hour), time.Minute) {
		t.Error("уже закоммиченный Pending не должен коммититься повторно")
	}
}

// CommitAckVersion — что нода репортит как «версия, которую я держу COMMITTED или
// применила». Анонс (ApplyAt=0) НЕ считается committed — иначе seed решил бы, что нода
// готова флипать, когда она лишь получила анонс (исходный strand-баг).
func TestCommitAckVersion(t *testing.T) {
	cases := []struct {
		name string
		s    *state.State
		want uint64
	}{
		{"нет Pending — cv = применённая версия", &state.State{ParamsVersion: 3}, 3},
		{"announced (ApplyAt=0) НЕ считается committed",
			&state.State{ParamsVersion: 3, Pending: pending(4)}, 3},
		{"committed новее применённой — cv = версия Pending",
			&state.State{ParamsVersion: 3, Pending: committed(4)}, 4},
		{"committed не новее применённой — cv = применённая (max)",
			&state.State{ParamsVersion: 5, Pending: committed(4)}, 5},
	}
	for _, c := range cases {
		if got := CommitAckVersion(c.s); got != c.want {
			t.Errorf("%s: = %d, want %d", c.name, got, c.want)
		}
	}
}

// AnnounceAckVersion — что нода репортит как «видела анонс до версии»: максимум из
// применённой и любого Pending (в т.ч. announced, в отличие от CommitAckVersion).
func TestAnnounceAckVersion(t *testing.T) {
	cases := []struct {
		name string
		s    *state.State
		want uint64
	}{
		{"нет Pending — применённая", &state.State{ParamsVersion: 3}, 3},
		{"announced считается (в отличие от commit-ack)",
			&state.State{ParamsVersion: 3, Pending: pending(4)}, 4},
		{"committed тоже считается", &state.State{ParamsVersion: 3, Pending: committed(5)}, 5},
		{"Pending не новее применённой — применённая", &state.State{ParamsVersion: 6, Pending: pending(4)}, 6},
	}
	for _, c := range cases {
		if got := AnnounceAckVersion(c.s); got != c.want {
			t.Errorf("%s: = %d, want %d", c.name, got, c.want)
		}
	}
}

// NextPendingVersion — версия следующего анонса. Должна расти и при ретрае поверх
// уже висящего Pending (abort переанонсит v+1), иначе abort переиспользовал бы ту же
// версию и не был бы принят как «строго новее».
func TestNextPendingVersion(t *testing.T) {
	cases := []struct {
		name    string
		applied uint64
		pending *state.PendingParams
		want    uint64
	}{
		{"нет Pending — applied+1", 7, nil, 8},
		{"поверх анонса — pending.Version+1", 7, pending(8), 9},
		{"поверх committed — pending.Version+1", 7, committed(8), 9},
		{"вырожденный pending<=applied — applied+1", 7, pending(5), 8},
	}
	for _, c := range cases {
		if got := NextPendingVersion(c.applied, c.pending); got != c.want {
			t.Errorf("%s: = %d, want %d", c.name, got, c.want)
		}
	}
}

// ShouldAbort — отменять ли застрявший flip. Abort только для committed Pending, у
// которого НЕ все подтвердили приём ApplyAt И настал дедлайн решения (ApplyAt-margin).
func TestShouldAbort(t *testing.T) {
	t0 := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	const margin = 2 * time.Minute
	c := &state.PendingParams{Version: 8, ApplyAt: t0} // committed на t0
	cases := []struct {
		name           string
		p              *state.PendingParams
		now            time.Time
		allCommitAcked bool
		want           bool
	}{
		{"nil — нечего абортить", nil, t0, false, false},
		{"announced (ApplyAt=0) — нечего абортить", pending(8), t0, false, false},
		{"armed (все commit-acked) — не абортим, летим", c, t0.Add(-time.Second), true, false},
		{"до дедлайна (now < ApplyAt-margin) — рано", c, t0.Add(-3 * time.Minute), false, false},
		{"ровно дедлайн (now = ApplyAt-margin), не armed — abort", c, t0.Add(-margin), false, true},
		{"после дедлайна, не armed — abort", c, t0.Add(-time.Minute), false, true},
	}
	for _, cc := range cases {
		if got := ShouldAbort(cc.p, cc.now, cc.allCommitAcked, margin); got != cc.want {
			t.Errorf("%s: = %v, want %v", cc.name, got, cc.want)
		}
	}
}

// ShouldFire — флипать ли ноде в ApplyAt. Помимо due (PendingDue) требует, чтобы нода
// ДЕРЖАЛА committed ApplyAt не меньше abortMargin (узнала вовремя — любой abort бы
// дошёл). Коммит, полученный впритык к ApplyAt, не флипаем: возможен abort в полёте.
func TestShouldFire(t *testing.T) {
	t0 := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	const (
		margin   = 2 * time.Minute
		maxStale = 4 * time.Minute
	)
	c := &state.PendingParams{Version: 8, ApplyAt: t0}
	early := t0.Add(-3 * time.Minute) // получили коммит за 3мин (> margin) до ApplyAt
	late := t0.Add(-time.Minute)      // получили за 1мин (< margin) — впритык
	cases := []struct {
		name   string
		p      *state.PendingParams
		seenAt time.Time
		now    time.Time
		want   bool
	}{
		{"nil — нет", nil, early, t0, false},
		{"announced — нет (не due)", pending(8), early, t0, false},
		{"committed, держим давно, ровно ApplyAt — флип", c, early, t0, true},
		{"committed, держим давно, чуть после ApplyAt — флип", c, early, t0.Add(time.Second), true},
		{"committed, получили впритык (< margin) — НЕ флип (возможен abort)", c, late, t0, false},
		{"committed, seenAt нулевой (не записали) — НЕ флип", c, time.Time{}, t0, false},
		{"committed, ещё до ApplyAt — НЕ флип", c, early, t0.Add(-time.Second), false},
		{"committed, протух (> maxStale) — НЕ флип даже если держим давно", c, early, t0.Add(5 * time.Minute), false},
		{"committed, ровно граница margin (seenAt = ApplyAt-margin) — флип", c, t0.Add(-margin), t0, true},
	}
	for _, cc := range cases {
		if got := ShouldFire(cc.p, cc.seenAt, cc.now, margin, maxStale); got != cc.want {
			t.Errorf("%s: = %v, want %v", cc.name, got, cc.want)
		}
	}
}
