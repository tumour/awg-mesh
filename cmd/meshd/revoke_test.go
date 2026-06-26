package main

import (
	"path/filepath"
	"testing"

	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
)

func seedWithPeers(t *testing.T, path string) {
	t.Helper()
	s := &state.State{
		NodeLabel:   "seed",
		IsSeed:      true,
		NetworkCIDR: "100.64.0.0/24",
		PublicKey:   "SEEDPUB",
		NodeIP:      "100.64.0.1",
		Peers: []state.Peer{
			{Label: "orphan", PublicKey: "ORPH", NodeIP: "100.64.0.4", Endpoint: "1.2.3.4:51820"},
			{Label: "node-b", PublicKey: "BPUB", NodeIP: "100.64.0.5"},
		},
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
}

func countTombstone(ts []state.Tombstone, pub string) int {
	n := 0
	for _, t := range ts {
		if t.PublicKey == pub {
			n++
		}
	}
	return n
}

func TestCmdRevoke(t *testing.T) {
	dir := t.TempDir()

	t.Run("seed отзывает по mesh-IP — tombstone оседает, peers не тронуты (демон уберёт)", func(t *testing.T) {
		sf := filepath.Join(dir, "seed.json")
		seedWithPeers(t, sf)
		if err := cmdRevoke([]string{"--state-file", sf, "--yes", "100.64.0.4"}); err != nil {
			t.Fatalf("revoke: %v", err)
		}
		s, _ := state.Load(sf)
		if !mesh.IsRevoked(s.Tombstones, "ORPH") {
			t.Fatalf("tombstone на ORPH не оседал: %+v", s.Tombstones)
		}
		if len(s.Peers) != 2 {
			t.Fatalf("revoke НЕ должен трогать peers (их снимет демон), got %d", len(s.Peers))
		}
	})

	t.Run("селектор по полному pubkey тоже работает", func(t *testing.T) {
		sf := filepath.Join(dir, "bykey.json")
		seedWithPeers(t, sf)
		if err := cmdRevoke([]string{"--state-file", sf, "--yes", "BPUB"}); err != nil {
			t.Fatalf("revoke by pubkey: %v", err)
		}
		s, _ := state.Load(sf)
		if !mesh.IsRevoked(s.Tombstones, "BPUB") {
			t.Fatal("revoke по pubkey не сработал")
		}
	})

	t.Run("regular-нода отказывает", func(t *testing.T) {
		rf := filepath.Join(dir, "reg.json")
		if err := (&state.State{NodeLabel: "r", IsSeed: false, NodeIP: "100.64.0.2"}).Save(rf); err != nil {
			t.Fatal(err)
		}
		if err := cmdRevoke([]string{"--state-file", rf, "--yes", "100.64.0.4"}); err == nil {
			t.Fatal("ожидалась ошибка: revoke не на seed")
		}
	})

	t.Run("не найденный селектор — ошибка, tombstone не оседает", func(t *testing.T) {
		sf := filepath.Join(dir, "nf.json")
		seedWithPeers(t, sf)
		if err := cmdRevoke([]string{"--state-file", sf, "--yes", "100.64.0.99"}); err == nil {
			t.Fatal("ожидалась ошибка: нода не найдена")
		}
		s, _ := state.Load(sf)
		if len(s.Tombstones) != 0 {
			t.Fatalf("при ошибке tombstone не должен оседать: %+v", s.Tombstones)
		}
	})

	t.Run("идемпотентность — повторный revoke не дублирует tombstone", func(t *testing.T) {
		sf := filepath.Join(dir, "idem.json")
		seedWithPeers(t, sf)
		_ = cmdRevoke([]string{"--state-file", sf, "--yes", "100.64.0.4"})
		_ = cmdRevoke([]string{"--state-file", sf, "--yes", "100.64.0.4"})
		s, _ := state.Load(sf)
		if n := countTombstone(s.Tombstones, "ORPH"); n != 1 {
			t.Fatalf("повторный revoke продублировал tombstone: %d", n)
		}
	})

	t.Run("self-revoke отказывает", func(t *testing.T) {
		sf := filepath.Join(dir, "self.json")
		s := &state.State{
			NodeLabel: "seed", IsSeed: true, NetworkCIDR: "100.64.0.0/24",
			PublicKey: "SEEDPUB", NodeIP: "100.64.0.1",
			// аномалия: self затесался в peers — revoke по своему pubkey должен отказать
			Peers: []state.Peer{{Label: "seed", PublicKey: "SEEDPUB", NodeIP: "100.64.0.1"}},
		}
		if err := s.Save(sf); err != nil {
			t.Fatal(err)
		}
		if err := cmdRevoke([]string{"--state-file", sf, "--yes", "SEEDPUB"}); err == nil {
			t.Fatal("ожидалась ошибка: нельзя revoke себя")
		}
	})
}

func TestCmdLeave(t *testing.T) {
	dir := t.TempDir()

	t.Run("seed отказывает", func(t *testing.T) {
		sf := filepath.Join(dir, "seed.json")
		if err := (&state.State{NodeLabel: "seed", IsSeed: true, NodeIP: "100.64.0.1"}).Save(sf); err != nil {
			t.Fatal(err)
		}
		if err := cmdLeave([]string{"--state-file", sf, "--yes"}); err == nil {
			t.Fatal("ожидалась ошибка: leave на seed не поддержан")
		}
	})

	t.Run("regular — свой tombstone оседает локально (push best-effort, без endpoint-пиров не пытается)", func(t *testing.T) {
		sf := filepath.Join(dir, "reg.json")
		s := &state.State{
			NodeLabel: "node-b", IsSeed: false, NetworkCIDR: "100.64.0.0/24",
			PublicKey: "BPUB", NodeIP: "100.64.0.5",
			// пир без endpoint → не gossip-кандидат → push не пытается (тест быстрый),
			// но СВОЙ tombstone обязан осесть локально.
			Peers: []state.Peer{{Label: "seed", PublicKey: "SEEDPUB", NodeIP: "100.64.0.1"}},
		}
		if err := s.Save(sf); err != nil {
			t.Fatal(err)
		}
		if err := cmdLeave([]string{"--state-file", sf, "--yes"}); err != nil {
			t.Fatalf("leave не должен валиться (best-effort): %v", err)
		}
		got, _ := state.Load(sf)
		if !mesh.IsRevoked(got.Tombstones, "BPUB") {
			t.Fatal("leave должен записать СВОЙ tombstone локально")
		}
	})
}
