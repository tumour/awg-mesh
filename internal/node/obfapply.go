package node

import (
	"context"
	"log/slog"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// obfApplier (на каждой ноде) применяет к живому awg0 obf, который seed разложил в state
// через push (handleObf пишет LocalObf+ObfVersion, не трогая device — как handleTombstone).
// Reconciler сравнивает state.ObfVersion со своей применённой версией и при росте делает
// device.ApplyObf на лету (I1 не рвёт туннель). appliedVersion — in-memory, трогается ТОЛЬКО
// из tick (один goroutine — без локов); на старте = версии boot-конфига (Configure уже
// применил LocalObf при подъёме awg0), так что reconciler реагирует лишь на push-обновления.
type obfApplier struct {
	store          *state.Store
	dev            Device
	log            *slog.Logger
	appliedVersion uint64
}

// newObfApplier — applier с начальной applied-версией initialVersion (обычно state.ObfVersion
// на момент boot, уже применённой через Configure).
func newObfApplier(store *state.Store, dev Device, initialVersion uint64, logger *slog.Logger) *obfApplier {
	return &obfApplier{store: store, dev: dev, appliedVersion: initialVersion, log: logger}
}

// tick применяет obf, если в state накопилась более новая версия. Идемпотентно.
func (a *obfApplier) tick() {
	st, err := a.store.Read()
	if err != nil {
		a.log.Error("obf-apply: reload state failed", "err", err)
		return
	}
	if st.ObfVersion <= a.appliedVersion {
		return // уже применено
	}
	if err := a.dev.ApplyObf(st.LocalObf); err != nil {
		a.log.Error("obf-apply: ApplyObf failed", "err", err)
		return // не помечаем применённым → ретрай на следующем тике
	}
	a.log.Info("obf applied to device", "version", st.ObfVersion)
	a.appliedVersion = st.ObfVersion
}

// runObfApply тикает obfApplier.tick до отмены ctx.
func runObfApply(ctx context.Context, a *obfApplier, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.tick()
		}
	}
}
