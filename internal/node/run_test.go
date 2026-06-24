package node

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// fakeDevice — node.Device без реального TUN.
type fakeDevice struct {
	name       string
	configured bool
	upped      bool
	closed     bool
}

func (f *fakeDevice) Configure(wgkey.Private, awgparams.Params, []state.Peer, wgkey.Public) error {
	f.configured = true
	return nil
}
func (f *fakeDevice) UpdatePeer(state.Peer) error { return nil }
func (f *fakeDevice) Up() error                   { f.upped = true; return nil }
func (f *fakeDevice) Name() string                { return f.name }
func (f *fakeDevice) Close()                      { f.closed = true }

// fakeLinker — записывает порядок ОС-операций линка (вместо реального exec ip).
type fakeLinker struct {
	mu  sync.Mutex
	ops []string
}

func (l *fakeLinker) record(op string) { l.mu.Lock(); l.ops = append(l.ops, op); l.mu.Unlock() }
func (l *fakeLinker) AddIP(iface, cidr string) error {
	l.record("AddIP " + iface + " " + cidr)
	return nil
}
func (l *fakeLinker) SetUp(iface string) error   { l.record("SetUp " + iface); return nil }
func (l *fakeLinker) SetDown(iface string) error { l.record("SetDown " + iface); return nil }
func (l *fakeLinker) Delete(iface string) error  { l.record("Delete " + iface); return nil }

// TestRunOrchestratesDeviceAndLinker доказывает, что run-flow прогоняется с
// фейками (без TUN/root): Run настраивает userspace-device и дёргает Linker в
// правильном порядке. Это и есть «честность» узкого Device-интерфейса +
// инъекций Options.NewDevice/Linker.
func TestRunOrchestratesDeviceAndLinker(t *testing.T) {
	priv, err := wgkey.GeneratePrivate()
	if err != nil {
		t.Fatal(err)
	}
	params, err := awgparams.Generate()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.json")
	st := &state.State{
		NodeLabel:   "test",
		AwgParams:   params,
		NetworkCIDR: "100.64.0.0/24",
		PrivateKey:  priv.String(),
		PublicKey:   priv.Public().String(),
		NodeIP:      "127.0.0.1",
		ListenPort:  0,
		IsSeed:      false, // не seed → без bootstrap-listener
	}
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}

	dev := &fakeDevice{name: "awgtest0"}
	lnk := &fakeLinker{}

	// Pre-cancelled ctx: вся синхронная настройка отрабатывает, затем Run сразу
	// проходит <-ctx.Done() и завершается (детерминированно, без sleep).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = Run(ctx, Options{
		StateFile:      path,
		Interface:      "awg0",
		GossipInterval: 0, // gossip-клиент выключен
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewDevice:      func(string, int, int) (Device, error) { return dev, nil },
		Linker:         lnk,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !dev.configured || !dev.upped || !dev.closed {
		t.Errorf("device lifecycle incomplete: configured=%v upped=%v closed=%v",
			dev.configured, dev.upped, dev.closed)
	}

	// Delete по запрошенному имени (awg0), остальное по реальному (awgtest0).
	// SetDown на shutdown НЕ зовём — device.Close() (defer) удаляет TUN целиком.
	want := []string{
		"Delete awg0",
		"AddIP awgtest0 127.0.0.1/24",
		"SetUp awgtest0",
	}
	if !reflect.DeepEqual(lnk.ops, want) {
		t.Errorf("linker ops\n  got:  %v\n  want: %v", lnk.ops, want)
	}
}
