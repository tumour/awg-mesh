package mesh

import (
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

func newSeedState() *state.State {
	return &state.State{
		NetworkCIDR: "100.64.0.0/24",
		Peers:       []state.Peer{{Label: "seed", PublicKey: "SEED", NodeIP: "100.64.0.1", IsSeed: true}},
	}
}

func TestRegisterPeerNew(t *testing.T) {
	s := newSeedState()
	reg, err := RegisterPeer(s, JoinRequest{Label: "n1", PublicKey: "N1", Endpoint: "1.2.3.4:51820"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if reg.Rejoined || !reg.Changed {
		t.Fatalf("new peer: want Rejoined=false Changed=true, got %+v", reg)
	}
	if reg.AssignedIP != "100.64.0.2" {
		t.Fatalf("want .2 for first new peer, got %s", reg.AssignedIP)
	}
	if len(s.Peers) != 2 {
		t.Fatalf("peer not appended: %+v", s.Peers)
	}
}

// Отозванную ноду нельзя вернуть re-join'ом с тем же ключом — иначе revoke
// обходился бы через bootstrap в обход gossip-перекрытия.
func TestRegisterPeerRevokedRejected(t *testing.T) {
	s := newSeedState()
	s.Tombstones = []state.Tombstone{{PublicKey: "N1", Label: "n1"}}

	_, err := RegisterPeer(s, JoinRequest{Label: "n1", PublicKey: "N1", Endpoint: "1.2.3.4:51820"})
	if err == nil {
		t.Fatal("re-join отозванной ноды должен быть отклонён")
	}
	if len(s.Peers) != 1 { // только seed, отозванный не добавлен
		t.Fatalf("отозванный не должен попасть в peers: %+v", s.Peers)
	}
}

func TestRegisterPeerRejoinNoChange(t *testing.T) {
	s := newSeedState()
	s.Peers = append(s.Peers, state.Peer{Label: "n1", PublicKey: "N1", NodeIP: "100.64.0.2", Endpoint: "1.2.3.4:51820"})

	// тот же pubkey, тот же endpoint → re-join без изменений
	reg, err := RegisterPeer(s, JoinRequest{Label: "n1", PublicKey: "N1", Endpoint: "1.2.3.4:51820"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !reg.Rejoined || reg.Changed {
		t.Fatalf("re-join same: want Rejoined=true Changed=false, got %+v", reg)
	}
	if reg.AssignedIP != "100.64.0.2" {
		t.Fatalf("must return existing IP, got %s", reg.AssignedIP)
	}
	if len(s.Peers) != 2 {
		t.Fatalf("re-join must not append, got %d peers", len(s.Peers))
	}
}

func TestRegisterPeerRejoinNewEndpoint(t *testing.T) {
	s := newSeedState()
	s.Peers = append(s.Peers, state.Peer{Label: "n1", PublicKey: "N1", NodeIP: "100.64.0.2", Endpoint: "old:1"})

	reg, err := RegisterPeer(s, JoinRequest{Label: "n1", PublicKey: "N1", Endpoint: "new:2"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if !reg.Rejoined || !reg.Changed {
		t.Fatalf("re-join new endpoint: want Rejoined=true Changed=true, got %+v", reg)
	}
	if s.Peers[1].Endpoint != "new:2" {
		t.Fatalf("endpoint not updated: %+v", s.Peers[1])
	}
}

func TestRegisterPeerRejoinEmptyEndpointKeepsOld(t *testing.T) {
	s := newSeedState()
	s.Peers = append(s.Peers, state.Peer{Label: "n1", PublicKey: "N1", NodeIP: "100.64.0.2", Endpoint: "known:51820"})

	// resume без endpoint не должен затирать объявленный ранее
	reg, err := RegisterPeer(s, JoinRequest{Label: "n1", PublicKey: "N1", Endpoint: ""})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if reg.Changed {
		t.Fatalf("empty endpoint must not change state, got %+v", reg)
	}
	if s.Peers[1].Endpoint != "known:51820" {
		t.Fatalf("empty endpoint erased old: %+v", s.Peers[1])
	}
}
