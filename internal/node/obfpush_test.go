package node

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tumour/awg-mesh/internal/gossip"
	"github.com/tumour/awg-mesh/internal/state"
)

// recordingPush — фейковый транспорт push'а: пишет доставленные пуши, опционально роняет
// заданный IP (имитация недостижимой ноды для проверки ретраев).
type recordingPush struct {
	mu     sync.Mutex
	calls  []gossip.ObfPush
	ips    []string
	failIP string
}

func (r *recordingPush) fn(_ context.Context, meshIP string, p gossip.ObfPush) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if meshIP == r.failIP {
		return errors.New("unreachable")
	}
	r.calls = append(r.calls, p)
	r.ips = append(r.ips, meshIP)
	return nil
}

func (r *recordingPush) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func newObfPusher(t *testing.T, st *state.State, push *recordingPush) *obfPusher {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	return &obfPusher{
		store:     state.NewStore(path),
		selfPub:   st.PublicKey,
		push:      push.fn,
		genI1:     func(sni string) (string, error) { return "I1(" + sni + ")", nil },
		log:       quietLogger(),
		confirmed: map[string]uint64{},
	}
}

// Раздаёт текущую версию всем пирам с mesh-IP (кроме себя и безадресных), повторный тик
// уже подтверждённых НЕ пушит.
func TestObfPusher_PushesUnconfirmedThenStops(t *testing.T) {
	st := &state.State{
		PublicKey: "SELF", NodeIP: "100.64.0.1",
		ObfPolicy: &state.ObfPolicy{SNI: "x.org", Version: 3},
		Peers: []state.Peer{
			{Label: "a", PublicKey: "A", NodeIP: "100.64.0.2"},
			{Label: "b", PublicKey: "B", NodeIP: "100.64.0.3"},
			{Label: "self", PublicKey: "SELF", NodeIP: "100.64.0.1"}, // себя — пропустить
			{Label: "noip", PublicKey: "C", NodeIP: ""},              // без mesh-IP — пропустить
		},
	}
	push := &recordingPush{}
	p := newObfPusher(t, st, push)

	p.tick(context.Background())
	if push.count() != 2 {
		t.Fatalf("want 2 pushes (только адресуемые пиры), got %d (%v)", push.count(), push.ips)
	}
	for _, c := range push.calls {
		if c.Version != 3 || c.I1 != "I1(x.org)" {
			t.Fatalf("bad push payload: %+v", c)
		}
	}

	p.tick(context.Background()) // все подтверждены — повторно не пушим
	if push.count() != 2 {
		t.Fatalf("подтверждённых пиров перепушили: count=%d", push.count())
	}
}

// Недостижимая нода НЕ метится подтверждённой → следующий тик ретраит.
func TestObfPusher_RetriesOnFailure(t *testing.T) {
	st := &state.State{
		PublicKey: "SELF", NodeIP: "100.64.0.1",
		ObfPolicy: &state.ObfPolicy{SNI: "x.org", Version: 1},
		Peers:     []state.Peer{{Label: "a", PublicKey: "A", NodeIP: "100.64.0.2"}},
	}
	push := &recordingPush{failIP: "100.64.0.2"}
	p := newObfPusher(t, st, push)

	p.tick(context.Background()) // падает → не подтверждён, ничего не доставлено
	if push.count() != 0 {
		t.Fatalf("упавший push не должен числиться доставленным: %d", push.count())
	}
	push.failIP = "" // нода вернулась
	p.tick(context.Background())
	if push.count() != 1 {
		t.Fatalf("ретрай не доставил: want 1, got %d", push.count())
	}
}

// Бамп версии политики → перепушить уже подтверждённым (получили новую версию).
func TestObfPusher_RepushOnVersionBump(t *testing.T) {
	st := &state.State{
		PublicKey: "SELF", NodeIP: "100.64.0.1",
		ObfPolicy: &state.ObfPolicy{SNI: "x.org", Version: 1},
		Peers:     []state.Peer{{Label: "a", PublicKey: "A", NodeIP: "100.64.0.2"}},
	}
	push := &recordingPush{}
	p := newObfPusher(t, st, push)

	p.tick(context.Background()) // v1 доставлен, подтверждён
	if _, err := p.store.Update(func(s *state.State) error { s.ObfPolicy.Version = 2; return nil }); err != nil {
		t.Fatalf("bump: %v", err)
	}
	p.tick(context.Background())
	if push.count() != 2 {
		t.Fatalf("при бампе версии не перепушили: count=%d", push.count())
	}
	if push.calls[1].Version != 2 {
		t.Fatalf("перепушили старую версию: %d", push.calls[1].Version)
	}
}

// Нет obf-политики → ничего не раздаём.
func TestObfPusher_NoPolicyNoPush(t *testing.T) {
	st := &state.State{
		PublicKey: "SELF", NodeIP: "100.64.0.1",
		Peers: []state.Peer{{Label: "a", PublicKey: "A", NodeIP: "100.64.0.2"}},
	}
	push := &recordingPush{}
	p := newObfPusher(t, st, push)

	p.tick(context.Background())
	if push.count() != 0 {
		t.Fatalf("без политики не должно быть push'ей: %d", push.count())
	}
}
