package node

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/gossip"
	"github.com/tumour/awg-mesh/internal/state"
)

// obfPusher (seed-only) активно раздаёт текущую obf-политику (ObfPolicy) всем пирам:
// для каждого, кто ещё не подтвердил текущую версию, генерит per-node уникальный I1 из
// SNI и пушит ему; на успешный ACK (gossip.PushObf вернул nil) метит подтверждённым.
// Бамп версии (новый set-params --sni) автоматически «протухляет» старые подтверждения
// (confirmed[peer] < version) → перераздача. confirmed — in-memory и трогается ТОЛЬКО из
// tick (один goroutine — без локов); рестарт seed'а перераздаст, приём идемпотентен.
type obfPusher struct {
	store     *state.Store
	selfPub   string
	push      func(ctx context.Context, meshIP string, p gossip.ObfPush) error
	genI1     func(sni string) (string, error)
	log       *slog.Logger
	confirmed map[string]uint64
}

// newSeedObfPusher собирает боевой pusher: реальный транспорт (gossip.PushObf по
// собственному http-клиенту) и генерацию I1 из crypto/rand (per-node уникальность —
// DPI не напишет один rule).
func newSeedObfPusher(store *state.Store, port int, selfPub string, logger *slog.Logger) *obfPusher {
	hc := &http.Client{Timeout: 10 * time.Second}
	return &obfPusher{
		store:   store,
		selfPub: selfPub,
		push: func(ctx context.Context, meshIP string, p gossip.ObfPush) error {
			return gossip.PushObf(ctx, hc, meshIP, port, p)
		},
		genI1: func(sni string) (string, error) {
			return awgparams.GenerateQUICInitialObf(sni, rand.Reader)
		},
		log:       logger,
		confirmed: map[string]uint64{},
	}
}

// tick раздаёт текущую версию политики неподтверждённым пирам. Идемпотентно: если все
// подтвердили — не делает ничего.
func (p *obfPusher) tick(ctx context.Context) {
	st, err := p.store.Read()
	if err != nil {
		p.log.Error("obf-push: reload state failed", "err", err)
		return
	}
	if st.ObfPolicy.Empty() {
		return // политика обхода не задана — раздавать нечего
	}
	version := st.ObfPolicy.Version
	for _, peer := range st.Peers {
		if peer.PublicKey == p.selfPub || peer.NodeIP == "" {
			continue // себя и безадресных не пушим
		}
		if p.confirmed[peer.PublicKey] >= version {
			continue // уже держит текущую версию
		}
		i1, err := p.policyI1(st.ObfPolicy)
		if err != nil {
			p.log.Error("obf-push: generate I1 failed", "err", err)
			continue
		}
		if err := p.push(ctx, peer.NodeIP, gossip.ObfPush{Version: version, I1: i1}); err != nil {
			p.log.Debug("obf-push pending", "peer", peer.Label, "err", err)
			continue // не подтверждён — ретрай на следующем тике
		}
		p.confirmed[peer.PublicKey] = version
	}
}

// policyI1 — I1 для раздачи по политике: сырой spec оператора как есть, иначе per-node
// генерация из SNI (уникальность per-node — DPI не напишет один rule).
func (p *obfPusher) policyI1(pol *state.ObfPolicy) (string, error) {
	if pol.I1 != "" {
		return pol.I1, nil
	}
	return p.genI1(pol.SNI)
}

// runObfPush тикает obfPusher.tick до отмены ctx (seed-only).
func runObfPush(ctx context.Context, p *obfPusher, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx)
		}
	}
}
