package mesh

import (
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

// Топология hub-and-spoke: vps — hub (есть endpoint), ax3200/flint2 — spoke (NAT,
// без endpoint). Между двумя spoke прямого пути нет.
func threeNodeState(selfPub string) *state.State {
	return &state.State{
		PublicKey:   selfPub,
		NetworkCIDR: "100.64.0.0/24",
		Peers: []state.Peer{
			{Label: "vps", PublicKey: "VPS", NodeIP: "100.64.0.1", Endpoint: "1.2.3.4:51820", IsSeed: true},
			{Label: "ax3200", PublicKey: "AX", NodeIP: "100.64.0.2"}, // NAT, без endpoint
			{Label: "flint2", PublicKey: "FLINT", NodeIP: "100.64.0.3"}, // NAT, без endpoint
		},
	}
}

func keysOf(peers []state.Peer) map[string]bool {
	m := make(map[string]bool, len(peers))
	for _, p := range peers {
		m[p.PublicKey] = true
	}
	return m
}

// Ядро фикса: spoke (мы за NAT) НЕ выбирает другой spoke (тоже за NAT) — пути нет.
// Кандидат должен остаться только hub (с endpoint).
func TestGossipCandidatesSpokeDoesNotPickSpoke(t *testing.T) {
	// Мы = ax3200 (spoke, без endpoint).
	s := threeNodeState("AX")
	got := keysOf(GossipCandidates(s))

	if !got["VPS"] {
		t.Fatal("spoke must keep the hub (vps has endpoint) as a gossip target")
	}
	if got["FLINT"] {
		t.Fatal("spoke must NOT pick another spoke (flint2 is NAT, no path) — this is the noise bug")
	}
	if got["AX"] {
		t.Fatal("must never target self")
	}
	if len(GossipCandidates(s)) != 1 {
		t.Fatalf("spoke should have exactly 1 candidate (the hub), got %d", len(GossipCandidates(s)))
	}
}

// Hub (у нас есть endpoint) может опрашивать всех — даже NAT-пиров, которые
// держат к нам встречный туннель.
func TestGossipCandidatesHubReachesAll(t *testing.T) {
	// Мы = vps (hub, с endpoint). Подменим self на запись с endpoint.
	s := &state.State{
		PublicKey: "VPS",
		Peers: []state.Peer{
			{Label: "vps", PublicKey: "VPS", NodeIP: "100.64.0.1", Endpoint: "1.2.3.4:51820", IsSeed: true},
			{Label: "ax3200", PublicKey: "AX", NodeIP: "100.64.0.2"},
			{Label: "flint2", PublicKey: "FLINT", NodeIP: "100.64.0.3"},
		},
	}
	got := keysOf(GossipCandidates(s))
	if !got["AX"] || !got["FLINT"] {
		t.Fatalf("hub (self has endpoint) must reach NAT spokes too, got %v", got)
	}
	if got["VPS"] {
		t.Fatal("must never target self")
	}
}

// Spoke с двумя hub'ами видит оба.
func TestGossipCandidatesSpokePicksAllHubs(t *testing.T) {
	s := &state.State{
		PublicKey: "AX",
		Peers: []state.Peer{
			{PublicKey: "AX", NodeIP: "100.64.0.2"}, // мы, NAT
			{PublicKey: "HUB1", NodeIP: "100.64.0.1", Endpoint: "1.1.1.1:51820"},
			{PublicKey: "HUB2", NodeIP: "100.64.0.5", Endpoint: "2.2.2.2:51820"},
			{PublicKey: "SPOKE2", NodeIP: "100.64.0.6"}, // другой NAT — отсечь
		},
	}
	got := keysOf(GossipCandidates(s))
	if !got["HUB1"] || !got["HUB2"] {
		t.Fatalf("spoke must keep all endpoint-bearing hubs, got %v", got)
	}
	if got["SPOKE2"] {
		t.Fatal("spoke must drop another NAT spoke")
	}
}

// Пир без NodeIP (некорректная/недорегистрированная запись) не кандидат.
func TestGossipCandidatesSkipsEmptyNodeIP(t *testing.T) {
	s := &state.State{
		PublicKey: "AX",
		Peers: []state.Peer{
			{PublicKey: "AX", NodeIP: "100.64.0.2"},
			{PublicKey: "HUB", NodeIP: "100.64.0.1", Endpoint: "1.1.1.1:51820"},
			{PublicKey: "NOIP", NodeIP: "", Endpoint: "3.3.3.3:51820"}, // endpoint есть, но IP нет
		},
	}
	got := keysOf(GossipCandidates(s))
	if got["NOIP"] {
		t.Fatal("peer without NodeIP must not be a gossip candidate")
	}
	if !got["HUB"] {
		t.Fatal("valid hub must remain")
	}
}
