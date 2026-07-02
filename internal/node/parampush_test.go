package node

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/gossip"
	"github.com/tumour/awg-mesh/internal/state"
)

// fakeParamPush — фейковый транспорт PushParams: возвращает ack, который дала бы
// реальная нода, адоптив push.Pending. Опционально роняет IP (недостижимость) либо
// роняет его ТОЛЬКО на committed-фазе (нода видела анонс, но ApplyAt не получила —
// ровно сценарий, изолировавший медленную ноду на старом наборе).
type fakeParamPush struct {
	mu              sync.Mutex
	applied         uint64 // версия, уже применённая «нодами» (база ack)
	failIP          string // всегда недостижим
	failCommittedIP string // недостижим только для committed Pending (ApplyAt назначен)
	calls           []gossip.ParamPush
	ips             []string
}

func (f *fakeParamPush) fn(_ context.Context, meshIP string, push gossip.ParamPush) (gossip.ParamAck, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if meshIP == f.failIP {
		return gossip.ParamAck{}, errors.New("unreachable")
	}
	if meshIP == f.failCommittedIP && !push.Pending.ApplyAt.IsZero() {
		return gossip.ParamAck{}, errors.New("unreachable during commit")
	}
	f.calls = append(f.calls, push)
	f.ips = append(f.ips, meshIP)
	return ackFor(f.applied, push), nil
}

func (f *fakeParamPush) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeParamPush) pushedIPs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ips...)
}

// ackFor — ack, который вернула бы нода с applied-версией, адоптив push.Pending:
// announce = max(applied, pending.Version); commit = applied, либо pending.Version,
// если пришёл committed (ApplyAt назначен) и он новее (анонс committed'ом НЕ считается).
func ackFor(applied uint64, push gossip.ParamPush) gossip.ParamAck {
	p := push.Pending
	ack := gossip.ParamAck{Announce: applied, Commit: applied}
	if p.Version > ack.Announce {
		ack.Announce = p.Version
	}
	if !p.ApplyAt.IsZero() && p.Version > ack.Commit {
		ack.Commit = p.Version
	}
	return ack
}

func newTestParamPusher(store *state.Store, push *fakeParamPush, grace, abortMargin time.Duration) *paramPusher {
	return &paramPusher{
		store: store, selfPub: "SELF", push: push.fn,
		grace: grace, abortMargin: abortMargin, log: quietLogger(),
		acks: map[string]uint64{}, commitAcks: map[string]uint64{},
	}
}

// Все пиры подтвердили АНОНС → seed назначает ApplyAt (commit). Себя не пушим.
func TestParamPusher_CommitsWhenAllAcked(t *testing.T) {
	store := seedStore(t, announced()) // Pending v2 announced; peers SELF/A/B; applied=1
	push := &fakeParamPush{applied: 1}
	p := newTestParamPusher(store, push, 30*time.Second, time.Second) // grace > abortMargin

	p.tick(context.Background(), time.Now().UTC())

	if push.count() != 2 {
		t.Fatalf("want 2 пуша (A,B; не self), got %d (%v)", push.count(), push.pushedIPs())
	}
	s, _ := store.Read()
	if s.Pending == nil || s.Pending.ApplyAt.IsZero() {
		t.Fatalf("все подтвердили анонс → ApplyAt должен быть назначен: %+v", s.Pending)
	}
	if s.Pending.Version != 2 {
		t.Errorf("commit не меняет версию: %d", s.Pending.Version)
	}
}

// ЯДРО ПРАВИЛА №1 на уровне раздачи: пока хоть один пир недостижим и не подтвердил
// анонс — ApplyAt НЕ назначается. Иначе seed ушёл бы на новый набор без него (strand).
func TestParamPusher_UnreachableBlocksCommit(t *testing.T) {
	store := seedStore(t, announced())
	push := &fakeParamPush{applied: 1, failIP: "100.64.0.3"} // B недостижим
	p := newTestParamPusher(store, push, 30*time.Second, time.Second)

	p.tick(context.Background(), time.Now().UTC())

	s, _ := store.Read()
	if s.Pending == nil || !s.Pending.ApplyAt.IsZero() {
		t.Fatalf("пока B молчит — commit запрещён: %+v", s.Pending)
	}
}

// РЕГРЕСС strand: анонс приняли все, но committed ApplyAt дошёл не до всех (B отвалился
// на committed-фазе). seed обязан ОТМЕНИТЬ flip (переанонс v3), а не уйти на новый набор
// в одиночку, изолировав B. Active-push доставляет abort надёжнее пассивного pull'а.
func TestParamPusher_AbortsWhenCommitNotAllAcked(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	store := seedStore(t, committedAt(2, now)) // уже committed v2, ApplyAt=now (дедлайн настал)
	push := &fakeParamPush{applied: 1, failCommittedIP: "100.64.0.3"}
	p := newTestParamPusher(store, push, 30*time.Second, time.Minute) // abortMargin=1мин

	p.tick(context.Background(), now)

	s, _ := store.Read()
	if s.Pending == nil || s.Pending.Version != 3 || !s.Pending.ApplyAt.IsZero() {
		t.Fatalf("не все commit-acked к дедлайну → переанонс v3 announced: %+v", s.Pending)
	}
}

// Нет Pending → ничего не раздаём и не решаем.
func TestParamPusher_NoPendingPushesNothing(t *testing.T) {
	store := seedStore(t, nil)
	push := &fakeParamPush{applied: 1}
	p := newTestParamPusher(store, push, 30*time.Second, time.Second)

	p.tick(context.Background(), time.Now().UTC())

	if push.count() != 0 {
		t.Fatalf("без Pending push не нужен: %d", push.count())
	}
}

// Пушим только адресуемых пиров: себя и пиров без mesh-IP пропускаем.
func TestParamPusher_SkipsSelfAndAddressless(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st := &state.State{
		PublicKey: "SELF", IsSeed: true, ParamsVersion: 1,
		Peers: []state.Peer{
			{PublicKey: "SELF", NodeIP: "100.64.0.1"},
			{PublicKey: "A", NodeIP: "100.64.0.2"},
			{PublicKey: "C", NodeIP: ""}, // безадресный — пропустить
		},
		Pending: announced(),
	}
	if err := st.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	push := &fakeParamPush{applied: 1}
	p := newTestParamPusher(state.NewStore(path), push, 30*time.Second, time.Second)

	p.tick(context.Background(), time.Now().UTC())

	if got := push.pushedIPs(); len(got) != 1 || got[0] != "100.64.0.2" {
		t.Fatalf("должен пушить только A (.2): %v", got)
	}
}

// runParamPush тикает до отмены ctx и доводит flag-day до commit (ApplyAt назначен).
func TestRunParamPush_CommitsThenStops(t *testing.T) {
	store := seedStore(t, announced())
	push := &fakeParamPush{applied: 1}
	p := newTestParamPusher(store, push, 30*time.Second, time.Second)
	startLoop(t, func(ctx context.Context) { runParamPush(ctx, p, 2*time.Millisecond) })

	waitFor(t, func() bool { s, _ := store.Read(); return s.Pending != nil && !s.Pending.ApplyAt.IsZero() })
}

// bumpVersion монотонен: версия пира не откатывается назад (иначе stale-low ack после
// abort'а сбросил бы подтверждение и спровоцировал лишний abort).
func TestBumpVersion_Monotonic(t *testing.T) {
	m := map[string]uint64{}
	bumpVersion(m, "A", 5)
	bumpVersion(m, "A", 3) // старее — игнор
	if m["A"] != 5 {
		t.Errorf("откатился назад: %d", m["A"])
	}
	bumpVersion(m, "A", 9)
	if m["A"] != 9 {
		t.Errorf("не вырос до 9: %d", m["A"])
	}
	bumpVersion(m, "", 7) // пустой ключ — игнор, без паники
	if _, ok := m[""]; ok {
		t.Error("пустой ключ не должен попадать в мапу")
	}
}
