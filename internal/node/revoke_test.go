package node

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

func findStatePeer(ps []state.Peer, key string) *state.Peer {
	for i := range ps {
		if ps[i].PublicKey == key {
			return &ps[i]
		}
	}
	return nil
}

// C1: reapRevoked снимает отозванных с device И из Peers НЕЗАВИСИМО от gossip-pull.
// Без него revoke не применяется при offline-таргете / в 2-нодовой сети / при
// NAT-leave (там doRound делает early-return и никогда не доходит до применения).
func TestReapRevoked(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &state.State{
		NodeLabel:   "n",
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   "SELF",
		NodeIP:      "100.64.0.1",
		Peers: []state.Peer{
			{Label: "live", PublicKey: "LIVE", NodeIP: "100.64.0.2"},
			{Label: "orphan", PublicKey: "ORPH", NodeIP: "100.64.0.4"},
		},
		Tombstones: []state.Tombstone{{PublicKey: "ORPH"}},
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(path)
	dev := &fakeDevice{}

	reapRevoked(store, dev, quietLogger())

	if got := dev.removedKeys(); len(got) != 1 || got[0] != "ORPH" {
		t.Fatalf("device.RemovePeer: got %v, want [ORPH]", got)
	}
	got, _ := store.Read()
	if findStatePeer(got.Peers, "ORPH") != nil {
		t.Fatal("ORPH должен быть убран из Peers")
	}
	if findStatePeer(got.Peers, "LIVE") == nil {
		t.Fatal("LIVE (не отозван) не должен пострадать")
	}

	// Идемпотентность по ЭФФЕКТУ: повторный reap держит state стабильным (ORPH не
	// воскресает в Peers). Device-reconcile может повторно дёрнуть RemovePeer(ORPH) —
	// это безвредно (UAPI no-op), поэтому проверяем стабильность state, а не call-count.
	dev2 := &fakeDevice{}
	reapRevoked(store, dev2, quietLogger())
	got2, _ := store.Read()
	if findStatePeer(got2.Peers, "ORPH") != nil {
		t.Fatal("ORPH не должен воскресать в Peers при повторном reap")
	}
}

// F1: если device.RemovePeer падает, reap НЕ «забывает» снять peer'а навсегда —
// device-reconcile keyed off tombstones (не Peers), поэтому следующий tick повторяет
// снятие даже после того, как peer уже вычищен из state.Peers.
func TestReapRetriesDeviceRemovalOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := &state.State{
		NodeLabel:   "n",
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   "SELF",
		NodeIP:      "100.64.0.1",
		Peers:       []state.Peer{{Label: "orphan", PublicKey: "ORPH", NodeIP: "100.64.0.4"}},
		Tombstones:  []state.Tombstone{{PublicKey: "ORPH"}},
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	store := state.NewStore(path)

	// tick 1: device-снятие падает; state-prune всё равно уберёт ORPH из Peers.
	dev := &fakeDevice{removePeerErr: errors.New("ipc boom")}
	reapRevoked(store, dev, quietLogger())
	if got := dev.removedKeys(); len(got) != 0 {
		t.Fatalf("при ошибке RemovePeer ничего не должно числиться снятым, got %v", got)
	}

	// tick 2: device «починился» — reap обязан ПОВТОРИТЬ снятие по tombstone,
	// несмотря на то, что ORPH уже не в Peers.
	dev.removePeerErr = nil
	reapRevoked(store, dev, quietLogger())
	if got := dev.removedKeys(); len(got) != 1 || got[0] != "ORPH" {
		t.Fatalf("reap должен ретраить снятие ORPH по tombstone, got %v", got)
	}
}

// C2: startup НЕ должен конфигурить отозванного peer'а на device. Иначе рестарт
// демона воскрешает revoked (Configure(s.Peers) пушит peer-list без фильтра
// tombstones), и отозванная нода снова получает живой туннель до первого doRound.
func TestRunPrunesRevokedOnStartup(t *testing.T) {
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
		IsSeed:      false,
		Peers: []state.Peer{
			{Label: "live", PublicKey: "LIVE", NodeIP: "100.64.0.2"},
			{Label: "orphan", PublicKey: "ORPH", NodeIP: "100.64.0.4"},
		},
		Tombstones: []state.Tombstone{{PublicKey: "ORPH"}},
	}
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}

	dev := &fakeDevice{name: "awgtest0"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // вся синхронная настройка отрабатывает, затем Run выходит по ctx.Done()

	err = Run(ctx, Options{
		StateFile:      path,
		Interface:      "awg0",
		GossipInterval: 0, // gossip выключен — проверяем именно startup-путь
		Logger:         quietLogger(),
		NewDevice:      func(string, int, int, int) (Device, error) { return dev, nil },
		Linker:         &fakeLinker{},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, k := range dev.configuredKeys() {
		if k == "ORPH" {
			t.Fatalf("отозванный ORPH попал в Configure — рестарт воскрешает revoked (C2); configured=%v", dev.configuredKeys())
		}
	}
	// И state на диске должен быть вычищен (иначе следующий старт снова с ORPH).
	got, _ := state.NewStore(path).Read()
	if findStatePeer(got.Peers, "ORPH") != nil {
		t.Fatal("startup должен вычистить отозванного из Peers на диске")
	}
}
