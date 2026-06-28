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

// fakeDevice — node.Device без реального TUN. Трекинг configured/updated/removed
// нужен, чтобы тесты ловили перепутанные callback'и (onNewPeers↔onRemovedPeers) и
// проверяли, что revoke реально снимает peer с device. Методы потокобезопасны:
// reap-loop и pushPeers могут трогать device из разных goroutine.
type fakeDevice struct {
	mu              sync.Mutex
	name            string
	configured      bool
	configuredObf   awgparams.LocalObf // что ушло в Configure (для проверки backfill I1)
	configuredPeers []state.Peer       // что ушло в Configure (для проверки startup-prune)
	appliedParams   bool
	applyParamsErr  error              // инъекция: ошибка из ApplyParams (для error-ветки flip)
	appliedObf      awgparams.LocalObf // что ушло в ApplyObf (проверка применения присланного I1)
	applyObfErr     error              // инъекция: ошибка из ApplyObf (для retry-ветки reconciler)
	removePeerErr   error    // инъекция: ошибка из RemovePeer (для retry-ветки reap)
	updated         []string // pubkeys через UpdatePeer
	removed         []string // pubkeys через RemovePeer
	upped           bool
	closed          bool
}

func (f *fakeDevice) Configure(_ wgkey.Private, _ awgparams.Params, lo awgparams.LocalObf, peers []state.Peer, _ wgkey.Public) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configured = true
	f.configuredObf = lo
	f.configuredPeers = append([]state.Peer(nil), peers...)
	return nil
}
func (f *fakeDevice) ApplyParams(awgparams.Params) error {
	f.appliedParams = true
	return f.applyParamsErr
}
func (f *fakeDevice) ApplyObf(lo awgparams.LocalObf) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyObfErr != nil {
		return f.applyObfErr
	}
	f.appliedObf = lo
	return nil
}
func (f *fakeDevice) UpdatePeer(p state.Peer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, p.PublicKey)
	return nil
}
func (f *fakeDevice) RemovePeer(pubkey string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removePeerErr != nil {
		return f.removePeerErr // снятие провалилось — НЕ трекаем как снятого
	}
	f.removed = append(f.removed, pubkey)
	return nil
}
func (f *fakeDevice) Up() error    { f.upped = true; return nil }
func (f *fakeDevice) Name() string { return f.name }
func (f *fakeDevice) Close()       { f.closed = true }

// removedKeys / configuredKeys — снимок под локом для ассертов.
func (f *fakeDevice) removedKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removed...)
}
func (f *fakeDevice) configuredKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.configuredPeers))
	for i, p := range f.configuredPeers {
		out[i] = p.PublicKey
	}
	return out
}

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
		NewDevice:      func(string, int, int, int) (Device, error) { return dev, nil },
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

// runOnce прогоняет Run с pre-cancelled ctx (вся синхронная настройка отрабатывает,
// затем завершение) и возвращает Store + device для ассертов состояния.
func runOnce(t *testing.T, st *state.State) (*state.Store, *fakeDevice) {
	t.Helper()
	priv, err := wgkey.GeneratePrivate()
	if err != nil {
		t.Fatal(err)
	}
	st.PrivateKey = priv.String()
	st.PublicKey = priv.Public().String()
	if st.NetworkCIDR == "" {
		st.NetworkCIDR = "100.64.0.0/24"
	}
	if st.NodeIP == "" {
		st.NodeIP = "127.0.0.1"
	}
	path := filepath.Join(t.TempDir(), "state.json")
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	dev := &fakeDevice{name: "awgtest0"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, Options{
		StateFile:      path,
		Interface:      "awg0",
		GossipInterval: 0,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		NewDevice:      func(string, int, int, int) (Device, error) { return dev, nil },
		Linker:         &fakeLinker{},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return state.NewStore(path), dev
}

// Мигрированная нода с ПУСТЫМ local_obf обязана получить DefaultI1 при старте — и в
// state, и на device. Без этого инициатор за DPI остаётся без QUIC-мимикрии.
func TestRunBackfillsDefaultObfWhenEmpty(t *testing.T) {
	params, _ := awgparams.Generate()
	store, dev := runOnce(t, &state.State{NodeLabel: "t", AwgParams: params}) // LocalObf пуст

	s, _ := store.Read()
	if s.LocalObf.I1 != awgparams.DefaultI1 {
		t.Fatalf("local_obf не backfill'ен в state: %+v", s.LocalObf)
	}
	if dev.configuredObf.I1 != awgparams.DefaultI1 {
		t.Fatalf("device сконфигурён без I1: %+v", dev.configuredObf)
	}
}

// Заданную вручную обфускацию backfill НЕ трогает (override под конкретный ISP важнее дефолта).
func TestRunKeepsExistingObf(t *testing.T) {
	params, _ := awgparams.Generate()
	custom := awgparams.LocalObf{I1: "<b 0xdeadbeef><t>"}
	store, dev := runOnce(t, &state.State{NodeLabel: "t", AwgParams: params, LocalObf: custom})

	s, _ := store.Read()
	if s.LocalObf != custom {
		t.Fatalf("backfill затёр пользовательский obf: %+v", s.LocalObf)
	}
	if dev.configuredObf != custom {
		t.Fatalf("device получил не пользовательский obf: %+v", dev.configuredObf)
	}
}
