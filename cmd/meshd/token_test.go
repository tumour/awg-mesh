package main

import (
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

func TestSeedBootstrapInfoOnSeed(t *testing.T) {
	s := &state.State{
		IsSeed:    true,
		PublicKey: "SEEDPUB",
		Peers: []state.Peer{
			{PublicKey: "SEEDPUB", Endpoint: "1.2.3.4:51820", IsSeed: true},
		},
	}
	pub, ep, err := seedBootstrapInfo(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pub != "SEEDPUB" || ep != "1.2.3.4:51820" {
		t.Fatalf("got pub=%q ep=%q", pub, ep)
	}
}

func TestSeedBootstrapInfoOnSeedWithoutEndpoint(t *testing.T) {
	// Seed без endpoint'а в своей peer-записи — токен был бы бесполезен.
	s := &state.State{
		IsSeed:    true,
		PublicKey: "SEEDPUB",
		Peers:     []state.Peer{{PublicKey: "SEEDPUB", IsSeed: true}},
	}
	if _, _, err := seedBootstrapInfo(s); err == nil {
		t.Fatal("want error for seed without endpoint, got nil")
	}
}

func TestSeedBootstrapInfoOnRegularNode(t *testing.T) {
	// Обычная нода берёт seed-инфо из peer-list (попало туда через gossip).
	s := &state.State{
		IsSeed:    false,
		PublicKey: "MYPUB",
		Peers: []state.Peer{
			{PublicKey: "MYPUB", NodeIP: "100.64.0.2"},
			{PublicKey: "SEEDPUB", Endpoint: "1.2.3.4:51820", NodeIP: "100.64.0.1", IsSeed: true},
		},
	}
	pub, ep, err := seedBootstrapInfo(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pub != "SEEDPUB" || ep != "1.2.3.4:51820" {
		t.Fatalf("got pub=%q ep=%q", pub, ep)
	}
}

func TestSeedBootstrapInfoNoSeed(t *testing.T) {
	s := &state.State{Peers: []state.Peer{{PublicKey: "MYPUB"}}}
	if _, _, err := seedBootstrapInfo(s); err == nil {
		t.Fatal("want error when no seed in state, got nil")
	}
}

func TestSeedBootstrapInfoSeedPeerNoEndpoint(t *testing.T) {
	// На обычной ноде seed в peer-list без endpoint'а — нечего раздавать.
	s := &state.State{
		PublicKey: "MYPUB",
		Peers:     []state.Peer{{PublicKey: "SEEDPUB", IsSeed: true}},
	}
	if _, _, err := seedBootstrapInfo(s); err == nil {
		t.Fatal("want error for seed peer without endpoint, got nil")
	}
}

func TestSeedBootstrapInfoPrefersSeedWithEndpoint(t *testing.T) {
	// Несколько seed: первый без endpoint, второй с — не падаем на первом,
	// берём пригодный.
	s := &state.State{
		PublicKey: "MYPUB",
		Peers: []state.Peer{
			{PublicKey: "SEED-A", IsSeed: true},                            // без endpoint
			{PublicKey: "SEED-B", Endpoint: "1.2.3.4:51820", IsSeed: true}, // с endpoint
		},
	}
	pub, ep, err := seedBootstrapInfo(s)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pub != "SEED-B" || ep != "1.2.3.4:51820" {
		t.Fatalf("want SEED-B with endpoint, got pub=%q ep=%q", pub, ep)
	}
}
