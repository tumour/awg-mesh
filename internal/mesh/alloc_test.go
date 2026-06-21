package mesh

import (
	"fmt"
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

func TestAllocateNextIPSequential(t *testing.T) {
	s := &state.State{
		NetworkCIDR: "100.64.0.0/24",
		Peers:       []state.Peer{{NodeIP: "100.64.0.1"}}, // seed
	}

	ip, err := AllocateNextIP(s)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	if ip != "100.64.0.2" {
		t.Fatalf("first regular peer must get .2, got %s", ip)
	}

	// .2 и .4 заняты — аллокатор берёт первую дырку
	s.Peers = append(s.Peers,
		state.Peer{NodeIP: "100.64.0.2"},
		state.Peer{NodeIP: "100.64.0.4"},
	)
	ip, err = AllocateNextIP(s)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	if ip != "100.64.0.3" {
		t.Fatalf("want first free .3, got %s", ip)
	}
}

func TestAllocateNextIPExhaustionSkips255(t *testing.T) {
	s := &state.State{NetworkCIDR: "100.64.0.0/24"}
	for i := 2; i <= 254; i++ {
		s.Peers = append(s.Peers, state.Peer{NodeIP: fmt.Sprintf("100.64.0.%d", i)})
	}
	// Всё до .254 занято, .255 (broadcast) не выдаётся → исчерпание
	if ip, err := AllocateNextIP(s); err == nil {
		t.Fatalf("want exhaustion error, got ip %s", ip)
	}
}

func TestAllocateNextIPBadCIDR(t *testing.T) {
	s := &state.State{NetworkCIDR: "not-a-cidr"}
	if _, err := AllocateNextIP(s); err == nil {
		t.Fatal("want parse error, got nil")
	}
}

func TestFirstUsableIP(t *testing.T) {
	got, err := FirstUsableIP("10.10.0.0/24")
	if err != nil {
		t.Fatalf("FirstUsableIP: %v", err)
	}
	if got != "10.10.0.1" {
		t.Fatalf("want 10.10.0.1, got %s", got)
	}
	if _, err := FirstUsableIP("not-a-cidr"); err == nil {
		t.Fatal("want error on bad cidr")
	}
}
