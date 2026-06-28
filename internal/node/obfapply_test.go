package node

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
)

func obfStore(t *testing.T, st *state.State) *state.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	return state.NewStore(path)
}

// Новее применённой → применяем присланный I1 к device и поднимаем applied-версию;
// повторный тик той же версии НЕ переприменяет.
func TestObfApplier_AppliesNewerVersion(t *testing.T) {
	store := obfStore(t, &state.State{ObfVersion: 5, LocalObf: awgparams.LocalObf{I1: "<b 0xnew>"}})
	dev := &fakeDevice{}
	a := newObfApplier(store, dev, 0, quietLogger())

	a.tick()
	if dev.appliedObf.I1 != "<b 0xnew>" {
		t.Fatalf("I1 не применён к device: %q", dev.appliedObf.I1)
	}
	if a.appliedVersion != 5 {
		t.Fatalf("applied-версия = %d, want 5", a.appliedVersion)
	}

	dev.appliedObf = awgparams.LocalObf{}
	a.tick() // та же версия — no-op
	if dev.appliedObf.I1 != "" {
		t.Fatalf("переприменили неизменную версию: %q", dev.appliedObf.I1)
	}
}

// Версия не новее уже применённой → не трогаем device.
func TestObfApplier_SkipsNotNewer(t *testing.T) {
	store := obfStore(t, &state.State{ObfVersion: 3, LocalObf: awgparams.LocalObf{I1: "<b 0xold>"}})
	dev := &fakeDevice{}
	a := newObfApplier(store, dev, 5, quietLogger()) // уже на 5 > 3

	a.tick()
	if dev.appliedObf.I1 != "" {
		t.Fatalf("применили не-новее: %q", dev.appliedObf.I1)
	}
}

// Ошибка ApplyObf → applied-версию НЕ поднимаем (ретрай на следующем тике).
func TestObfApplier_RetriesOnError(t *testing.T) {
	store := obfStore(t, &state.State{ObfVersion: 5, LocalObf: awgparams.LocalObf{I1: "<b 0xboom>"}})
	dev := &fakeDevice{applyObfErr: errors.New("ipc boom")}
	a := newObfApplier(store, dev, 0, quietLogger())

	a.tick() // падает
	if a.appliedVersion != 0 {
		t.Fatalf("при ошибке ApplyObf нельзя метить применённым: версия=%d", a.appliedVersion)
	}
	dev.applyObfErr = nil
	a.tick() // ретрай удался
	if a.appliedVersion != 5 || dev.appliedObf.I1 != "<b 0xboom>" {
		t.Fatalf("ретрай не применил: версия=%d i1=%q", a.appliedVersion, dev.appliedObf.I1)
	}
}
