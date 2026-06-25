package mesh

import (
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
)

func pending(v uint64) *state.PendingParams { return &state.PendingParams{Version: v} }

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
		{"remote == локального pending — повтор, no-op", 5, pending(7), pending(7), false},
		{"remote старее локального pending", 5, pending(8), pending(7), false},
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
		{"committed, после ApplyAt", committed, t0.Add(time.Second), true},
	}
	for _, c := range cases {
		if got := PendingDue(c.p, c.now); got != c.want {
			t.Errorf("%s: = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNewPending(t *testing.T) {
	p, _ := awgparams.Generate()
	pend := NewPending(7, p)

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
	if PendingDue(pend, time.Now()) {
		t.Error("announced Pending не должен быть due (нет ApplyAt)")
	}
}

func TestAllPeersAcked(t *testing.T) {
	const self = "SELF"
	peers := []state.Peer{
		{PublicKey: self, NodeIP: "100.64.0.1"},          // мы — пропускается
		{PublicKey: "A", NodeIP: "100.64.0.2"},           // должен ack
		{PublicKey: "B", NodeIP: "100.64.0.3"},           // должен ack
		{PublicKey: "GHOST", NodeIP: ""},                 // без mesh-IP — пропускается
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
