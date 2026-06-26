package gossip

import (
	"math/rand/v2"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// targetSelector выбирает, кого из gossip-кандидатов опрашивать, с учётом runtime-
// доступности. mesh.GossipCandidates отвечает «к кому есть путь ПО ДИЗАЙНУ» (есть
// endpoint); targetSelector — «кто отвечает ПРЯМО СЕЙЧАС». Стабильно-недостижимый
// endpoint-пир (например узел, чей прямой туннель душит DPI) иначе сжигал бы половину
// gossip-циклов на таймаут: pickRandomPeer выбирал бы его в ~½ случаев, каждый раз
// 10с впустую. Для flag-day это критично — committed ApplyAt обязан дойти до ВСЕХ в
// окне commitGrace, а вдвое более редкий pull seed'а узлом за NAT выбивает его из окна.
//
// Решение: экспоненциальный backoff по pubkey. Зафейленный пир временно исключается
// из выбора → узел сходится на живых каналах (seed) каждый цикл. Состояние in-memory,
// без persist: рестарт сбрасывает — это оптимизация выбора, не корректность.
// Доступ только из gossip Run-петли (doRound последователен) → без мьютекса.
type targetSelector struct {
	base  time.Duration // backoff после первого фейла
	ceil  time.Duration // потолок backoff (base * backoffCapFactor)
	state map[string]backoffState
}

// backoffState — сколько раз пир подряд зафейлил и до какого момента он не eligible.
type backoffState struct {
	fails int
	next  time.Time
}

// backoffCapFactor — во сколько base упирается потолок. При gossip-интервале 60с это
// 16мин: мёртвый пир пробуется ~раз в 16мин (детект восстановления), не каждый цикл.
const backoffCapFactor = 16

func newTargetSelector(gossipInterval time.Duration) *targetSelector {
	base := gossipInterval
	if base < time.Second {
		base = time.Second // пол для крошечных интервалов из тестов
	}
	return &targetSelector{
		base:  base,
		ceil:  base * backoffCapFactor,
		state: make(map[string]backoffState),
	}
}

// pick выбирает цель: случайного из готовых; если готовых нет — soonest-eligible
// (НЕ молчим: узел с единственным каналом обязан ретраить его каждый цикл). nil,
// только если кандидатов нет вовсе.
func (s *targetSelector) pick(candidates []state.Peer, now time.Time) *state.Peer {
	ready, soonest := s.eligible(candidates, now)
	if len(ready) > 0 {
		return pickRandomPeer(ready)
	}
	return soonest
}

// eligible делит кандидатов на готовых к опросу (нет backoff или он истёк) и
// возвращает запасного soonest — кандидата с самым ранним next среди ещё в backoff.
func (s *targetSelector) eligible(candidates []state.Peer, now time.Time) (ready []state.Peer, soonest *state.Peer) {
	var soonestAt time.Time
	for i := range candidates {
		p := candidates[i]
		b, backed := s.state[p.PublicKey]
		if !backed || !now.Before(b.next) {
			ready = append(ready, p)
			continue
		}
		if soonest == nil || b.next.Before(soonestAt) {
			pp := p
			soonest, soonestAt = &pp, b.next
		}
	}
	return ready, soonest
}

// recordFailure продлевает backoff пира: счётчик фейлов +1, next = now + duration.
func (s *targetSelector) recordFailure(pubkey string, now time.Time) {
	b := s.state[pubkey]
	b.fails++
	b.next = now.Add(s.duration(b.fails))
	s.state[pubkey] = b
}

// recordSuccess сбрасывает backoff: пир выздоровел → снова здоровый кандидат, а
// следующий фейл стартует заново с base (счётчик обнулён).
func (s *targetSelector) recordSuccess(pubkey string) {
	delete(s.state, pubkey)
}

// duration — backoff после fails подряд фейлов: base, 2·base, 4·base… до потолка ceil.
// Цикл останавливается на потолке → без переполнения при больших fails.
func (s *targetSelector) duration(fails int) time.Duration {
	if fails < 1 {
		fails = 1
	}
	d := s.base
	for i := 1; i < fails && d < s.ceil; i++ {
		d *= 2
	}
	if d > s.ceil {
		d = s.ceil
	}
	return d
}

// pickRandomPeer возвращает случайного пира из готового списка. nil — если пусто.
func pickRandomPeer(candidates []state.Peer) *state.Peer {
	if len(candidates) == 0 {
		return nil
	}
	picked := candidates[rand.IntN(len(candidates))]
	return &picked
}
