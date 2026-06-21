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

func TestAllocateNextIPNon24KeepsDotX255(t *testing.T) {
	// В /23 адрес 10.0.0.255 — ВАЛИДНЫЙ хост (broadcast = 10.0.1.255), его
	// нельзя пропускать как в /24. Занимаем .2..254 → следующий должен быть .255.
	s := &state.State{NetworkCIDR: "10.0.0.0/23"}
	for i := 2; i <= 254; i++ {
		s.Peers = append(s.Peers, state.Peer{NodeIP: fmt.Sprintf("10.0.0.%d", i)})
	}
	ip, err := AllocateNextIP(s)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	if ip != "10.0.0.255" {
		t.Fatalf("/23: .255 is a valid host, want 10.0.0.255, got %s", ip)
	}
}

func TestAllocateNextIPSkipsRealBroadcast(t *testing.T) {
	// /30: .0 network, .1/.2 hosts, .3 broadcast. Старт network+2 = .2; заняв
	// .2, ждём исчерпание (.3 — настоящий broadcast, не выдаётся).
	s := &state.State{NetworkCIDR: "10.0.0.0/30", Peers: []state.Peer{{NodeIP: "10.0.0.2"}}}
	if ip, err := AllocateNextIP(s); err == nil {
		t.Fatalf("/30: want exhaustion (.3 is broadcast), got %s", ip)
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
