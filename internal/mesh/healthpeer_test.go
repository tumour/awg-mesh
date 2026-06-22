package mesh

import (
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

func TestPickHealthPeerPrefersSeed(t *testing.T) {
	s := &state.State{
		NodeIP: "100.64.0.5",
		Peers: []state.Peer{
			{NodeIP: "100.64.0.5"}, // мы сами — пропустить
			{NodeIP: "100.64.0.9"}, // обычный пир — fallback
			{NodeIP: "100.64.0.1", IsSeed: true},
		},
	}
	if got := PickHealthPeer(s); got != "100.64.0.1" {
		t.Fatalf("want seed 100.64.0.1, got %q", got)
	}
}

func TestPickHealthPeerFallbackToAnyPeer(t *testing.T) {
	s := &state.State{
		NodeIP: "100.64.0.5",
		Peers: []state.Peer{
			{NodeIP: "100.64.0.5"}, // мы сами
			{NodeIP: "100.64.0.9"}, // единственный сосед
		},
	}
	if got := PickHealthPeer(s); got != "100.64.0.9" {
		t.Fatalf("want fallback 100.64.0.9, got %q", got)
	}
}

func TestPickHealthPeerAloneInMesh(t *testing.T) {
	s := &state.State{
		NodeIP: "100.64.0.1",
		Peers:  []state.Peer{{NodeIP: "100.64.0.1", IsSeed: true}},
	}
	if got := PickHealthPeer(s); got != "" {
		t.Fatalf("want empty (alone), got %q", got)
	}
}

func TestPickHealthPeerSkipsEmptyIP(t *testing.T) {
	// Пир без NodeIP (NAT-нода ещё без выделенного IP — теоретически) пропускается.
	s := &state.State{
		NodeIP: "100.64.0.5",
		Peers: []state.Peer{
			{NodeIP: "", IsSeed: true}, // нет IP — не годится для probe
			{NodeIP: "100.64.0.9"},
		},
	}
	if got := PickHealthPeer(s); got != "100.64.0.9" {
		t.Fatalf("want 100.64.0.9 (skip empty-IP seed), got %q", got)
	}
}
