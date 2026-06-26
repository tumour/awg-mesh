package gossip

import (
	"context"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
)

// Базовый момент времени для детерминизма (now инъектируется, не time.Now()).
var t0 = time.Unix(1_700_000_000, 0).UTC()

func peer(pub string) state.Peer {
	return state.Peer{PublicKey: pub, NodeIP: "100.64.0.9", Endpoint: "x:1"}
}

// pubsOf — множество pubkey'ев из списка пиров (для проверки членства без учёта порядка).
func pubsOf(peers []state.Peer) map[string]bool {
	m := make(map[string]bool, len(peers))
	for _, p := range peers {
		m[p.PublicKey] = true
	}
	return m
}

// Без фейлов все кандидаты eligible, запасного soonest нет.
func TestEligible_NoBackoff_AllReady(t *testing.T) {
	s := newTargetSelector(60 * time.Second)
	cands := []state.Peer{peer("A"), peer("B"), peer("C")}

	ready, soonest := s.eligible(cands, t0)
	if len(ready) != 3 {
		t.Fatalf("ready = %d peers, want 3", len(ready))
	}
	if soonest != nil {
		t.Fatalf("soonest = %v, want nil (никто не в backoff)", soonest.PublicKey)
	}
}

// Зафейленный пир исключён из ready до истечения backoff; на границе now==next — снова ready.
func TestEligible_FailedPeerExcludedUntilNext(t *testing.T) {
	s := newTargetSelector(60 * time.Second)
	cands := []state.Peer{peer("A"), peer("B")}

	s.recordFailure("A", t0)

	ready, soonest := s.eligible(cands, t0)
	if got := pubsOf(ready); got["A"] || !got["B"] {
		t.Fatalf("ready = %v, want только B (A в backoff)", got)
	}
	if soonest == nil || soonest.PublicKey != "A" {
		t.Fatalf("soonest = %v, want A", soonest)
	}

	// На границе now == next (now больше не Before next) — A снова eligible.
	atBoundary := t0.Add(60 * time.Second)
	ready, _ = s.eligible(cands, atBoundary)
	if !pubsOf(ready)["A"] {
		t.Fatalf("на границе now==next A обязан быть eligible, ready=%v", pubsOf(ready))
	}
}

// Длительность backoff растёт экспоненциально и упирается в потолок base*16.
func TestDuration_ExponentialWithCap(t *testing.T) {
	base := 60 * time.Second
	s := newTargetSelector(base)
	cases := []struct {
		fails int
		want  time.Duration
	}{
		{0, base},  // защита: <1 трактуется как 1
		{1, base},
		{2, 2 * base},
		{3, 4 * base},
		{4, 8 * base},
		{5, 16 * base},  // потолок
		{6, 16 * base},  // дальше не растёт
		{100, 16 * base},
	}
	for _, c := range cases {
		if got := s.duration(c.fails); got != c.want {
			t.Errorf("duration(%d) = %s, want %s", c.fails, got, c.want)
		}
	}
}

// Все кандидаты в backoff → ready пуст, soonest = пир с САМЫМ РАННИМ next.
func TestEligible_AllBackedOff_SoonestIsEarliest(t *testing.T) {
	s := newTargetSelector(60 * time.Second)
	cands := []state.Peer{peer("A"), peer("B")}

	s.recordFailure("A", t0)            // next = t0 + 60s
	s.recordFailure("B", t0)            // fails=1
	s.recordFailure("B", t0)            // fails=2 → next = t0 + 120s (позже A)

	ready, soonest := s.eligible(cands, t0)
	if len(ready) != 0 {
		t.Fatalf("ready = %v, want пусто (оба в backoff)", pubsOf(ready))
	}
	if soonest == nil || soonest.PublicKey != "A" {
		t.Fatalf("soonest = %v, want A (его next раньше)", soonest)
	}
}

// Одиночный кандидат, который фейлит: pick всё равно возвращает его (НЕ молчим) —
// иначе spoke с единственным каналом к seed замолчал бы навсегда после одного таймаута.
func TestPick_SingleFailingCandidate_NeverSilent(t *testing.T) {
	s := newTargetSelector(60 * time.Second)
	cands := []state.Peer{peer("seed")}

	for i := 0; i < 5; i++ {
		s.recordFailure("seed", t0)
		got := s.pick(cands, t0)
		if got == nil || got.PublicKey != "seed" {
			t.Fatalf("итерация %d: pick = %v, want seed (fallback на soonest)", i, got)
		}
	}
}

// pick при наличии готовых никогда не выбирает зафейленного.
func TestPick_PrefersReadyOverBackedOff(t *testing.T) {
	s := newTargetSelector(60 * time.Second)
	cands := []state.Peer{peer("dead"), peer("live1"), peer("live2")}
	s.recordFailure("dead", t0)

	for i := 0; i < 50; i++ {
		got := s.pick(cands, t0)
		if got == nil || got.PublicKey == "dead" {
			t.Fatalf("pick вернул %v — не должен выбирать dead, пока есть live", got)
		}
	}
}

// pick без кандидатов → nil (нечего госсипить).
func TestPick_NoCandidates_Nil(t *testing.T) {
	s := newTargetSelector(60 * time.Second)
	if got := s.pick(nil, t0); got != nil {
		t.Fatalf("pick(nil) = %v, want nil", got)
	}
}

// recordSuccess полностью сбрасывает backoff: пир снова eligible, а следующий фейл
// стартует с base (счётчик fails обнулён, не продолжает экспоненту).
func TestRecordSuccess_ResetsBackoff(t *testing.T) {
	s := newTargetSelector(60 * time.Second)
	cands := []state.Peer{peer("A")}

	s.recordFailure("A", t0) // fails=1
	s.recordFailure("A", t0) // fails=2 → next = t0+120s
	s.recordSuccess("A")

	ready, _ := s.eligible(cands, t0)
	if !pubsOf(ready)["A"] {
		t.Fatalf("после recordSuccess A обязан быть eligible, ready=%v", pubsOf(ready))
	}

	// Следующий фейл — снова base (60s), не 4*base: экспонента сброшена.
	s.recordFailure("A", t0)
	_, soonest := s.eligible(cands, t0)
	if soonest == nil {
		t.Fatal("A должен снова быть в backoff после нового фейла")
	}
	gotNext := s.state["A"].next
	if want := t0.Add(60 * time.Second); !gotNext.Equal(want) {
		t.Fatalf("next = %s, want %s (экспонента обязана сброситься на base)", gotNext.Sub(t0), want.Sub(t0))
	}
}

// Интеграция: фейл fetch в doRound записывает backoff на недостижимую цель — иначе
// узел продолжал бы выбирать её и жечь циклы на таймаут (находка #1).
func TestDoRound_FailedFetchRecordsBackoff(t *testing.T) {
	const (
		self    = "selfkey"
		deadKey = "deadkey"
	)
	// Поднимаем и СРАЗУ гасим сервер: коннект к закрытому порту падает быстро
	// (connection refused), без 10-сек HTTP-таймаута.
	host, port, ts := fakePeerServer(t, nil)
	ts.Close()

	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   self,
		NodeIP:      "100.64.0.1",
		Peers: []state.Peer{
			{Label: "dead", PublicKey: deadKey, NodeIP: host, Endpoint: "203.0.113.1:51820"},
		},
	})
	c := NewClient(store, self, time.Minute, port, func([]state.Peer) {}, nil, discardLogger())

	if _, backed := c.selector.state[deadKey]; backed {
		t.Fatal("precondition: backoff не должен существовать до раунда")
	}
	c.doRound(context.Background())

	b, backed := c.selector.state[deadKey]
	if !backed || b.fails != 1 {
		t.Fatalf("после фейла fetch ждём backoff fails=1, got backed=%v fails=%d", backed, b.fails)
	}
}

// Интеграция: успешный fetch снимает ранее накопленный backoff — иначе ожившая
// цель оставалась бы недо-приоритетной навсегда.
func TestDoRound_SuccessfulFetchClearsBackoff(t *testing.T) {
	const (
		self      = "selfkey"
		targetKey = "targetkey"
		newKey    = "newkey"
	)
	host, port, ts := fakePeerServer(t, []proto.PeerInfo{
		{Label: "newnode", PublicKey: newKey, NodeIP: "100.64.0.5", Endpoint: "198.51.100.7:51820"},
	})
	defer ts.Close()

	store := saveState(t, &state.State{
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   self,
		NodeIP:      "100.64.0.1",
		Peers: []state.Peer{
			{Label: "target", PublicKey: targetKey, NodeIP: host, Endpoint: "203.0.113.1:51820"},
		},
	})
	c := NewClient(store, self, time.Minute, port, func([]state.Peer) {}, nil, discardLogger())

	// Притворяемся, что цель раньше фейлила.
	c.selector.recordFailure(targetKey, time.Now())
	if _, backed := c.selector.state[targetKey]; !backed {
		t.Fatal("precondition: backoff на цель должен быть выставлен")
	}

	c.doRound(context.Background())

	if _, backed := c.selector.state[targetKey]; backed {
		t.Fatal("успешный fetch обязан снять backoff с цели")
	}
}
