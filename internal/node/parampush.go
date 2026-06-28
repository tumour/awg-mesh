package node

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/tumour/awg-mesh/internal/gossip"
	"github.com/tumour/awg-mesh/internal/state"
)

// paramPusher (seed-only) активно раздаёт текущий flag-day Pending каждой ноде и
// собирает её ack (announce/commit-версии) ПРЯМО из ответа — на смену пассивному
// gossip-pull управляющих сообщений (он давал livelock и изоляцию медленной ноды на
// старом наборе). По свежесобранным
// ack'ам на том же тике прогоняются доменные гейты: commit (назначить ApplyAt, когда
// все видели анонс) и arm/abort (отменить flip, если к дедлайну не все держат ApplyAt).
//
// acks/commitAcks — in-memory и монотонные, трогаются ТОЛЬКО из tick (один goroutine,
// без локов); рестарт seed'а пересоберёт их (push идемпотентен). Монотонность
// обязательна: stale-low ack после abort'а не должен сбрасывать подтверждение, а гейты
// всегда сверяются с ТЕКУЩЕЙ (большей) версией, так что устаревшее значение безвредно.
type paramPusher struct {
	store       *state.Store
	selfPub     string
	push        func(ctx context.Context, meshIP string, p gossip.ParamPush) (gossip.ParamAck, error)
	grace       time.Duration
	abortMargin time.Duration
	log         *slog.Logger
	acks        map[string]uint64
	commitAcks  map[string]uint64
}

// newSeedParamPusher собирает боевой pusher: реальный транспорт (gossip.PushParams по
// собственному http-клиенту). grace/abortMargin — окна flag-day (см. commitGraceFor/
// abortMarginFor), инвариант grace > abortMargin держит зазор для сбора commit-ack'ов.
func newSeedParamPusher(store *state.Store, port int, selfPub string, grace, abortMargin time.Duration, logger *slog.Logger) *paramPusher {
	hc := &http.Client{Timeout: 10 * time.Second}
	return &paramPusher{
		store:   store,
		selfPub: selfPub,
		push: func(ctx context.Context, meshIP string, p gossip.ParamPush) (gossip.ParamAck, error) {
			return gossip.PushParams(ctx, hc, meshIP, port, p)
		},
		grace:       grace,
		abortMargin: abortMargin,
		log:         logger,
		acks:        map[string]uint64{},
		commitAcks:  map[string]uint64{},
	}
}

// tick раздаёт текущий Pending неподтверждённым/всем пирам, обновляет ack'и из ответов
// и прогоняет commit/abort-гейты. Нет Pending → раздавать и решать нечего (no-op).
func (p *paramPusher) tick(ctx context.Context, now time.Time) {
	st, err := p.store.Read()
	if err != nil {
		p.log.Error("param-push: reload state failed", "err", err)
		return
	}
	if st.Pending == nil {
		return // активного flag-day нет
	}

	push := gossip.ParamPush{Pending: st.Pending}
	for _, peer := range st.Peers {
		if peer.PublicKey == p.selfPub || peer.NodeIP == "" {
			continue // себя и безадресных не пушим
		}
		ack, err := p.push(ctx, peer.NodeIP, push)
		if err != nil {
			p.log.Debug("param-push pending", "peer", peer.Label, "err", err)
			continue // нода не подтвердила — ретрай на следующем тике
		}
		bumpVersion(p.acks, peer.PublicKey, ack.Announce)
		bumpVersion(p.commitAcks, peer.PublicKey, ack.Commit)
	}

	// Те же доменные решения, что и при пассивном сборе ack'ов, но вход теперь прямой:
	// commit — когда все подтвердили АНОНС; abort — когда к дедлайну не все держат ApplyAt.
	commitIfAllAcked(p.store, p.acks, p.selfPub, p.grace, p.log)
	abortIfStuck(p.store, p.commitAcks, p.selfPub, p.abortMargin, now, p.log)
}

// runParamPush (seed-only) тикает paramPusher.tick до отмены ctx.
func runParamPush(ctx context.Context, p *paramPusher, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.tick(ctx, time.Now().UTC())
		}
	}
}

// bumpVersion монотонно поднимает версию пира в ack-мапе: откат назад невозможен
// (stale-low ack после abort'а/гонки не должен сбрасывать подтверждение). Пустой
// ключ игнорируем.
func bumpVersion(m map[string]uint64, key string, version uint64) {
	if key == "" {
		return
	}
	if version > m[key] {
		m[key] = version
	}
}
