package mesh

import (
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

// Топология hub-and-spoke: hub (есть endpoint), spoke-a/spoke-b (NAT, без endpoint).
// Между двумя spoke прямого пути нет.
func threeNodeState(selfPub string) *state.State {
	return &state.State{
		PublicKey:   selfPub,
		NetworkCIDR: "100.64.0.0/24",
		Peers: []state.Peer{
			{Label: "hub", PublicKey: "HUB", NodeIP: "100.64.0.1", Endpoint: "1.2.3.4:51820", IsSeed: true},
			{Label: "spoke-a", PublicKey: "SPOKEA", NodeIP: "100.64.0.2"}, // NAT, без endpoint
			{Label: "spoke-b", PublicKey: "SPOKEB", NodeIP: "100.64.0.3"}, // NAT, без endpoint
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
	// Мы = spoke-a (spoke, без endpoint).
	s := threeNodeState("SPOKEA")
	got := keysOf(GossipCandidates(s))

	if !got["HUB"] {
		t.Fatal("spoke must keep the hub (it has endpoint) as a gossip target")
	}
	if got["SPOKEB"] {
		t.Fatal("spoke must NOT pick another spoke (it is NAT, no path) — this is the noise bug")
	}
	if got["SPOKEA"] {
		t.Fatal("must never target self")
	}
	if len(GossipCandidates(s)) != 1 {
		t.Fatalf("spoke should have exactly 1 candidate (the hub), got %d", len(GossipCandidates(s)))
	}
}

// Регресс на БАГ #1 (полевой v0.4.0): hub (У НАС есть endpoint) тоже НЕ опрашивает
// NAT-spoke. Инициировать gossip-pull к узлу без endpoint нельзя — его адрес wg
// выучивает лишь динамически и теряет при рестарте (→ спам «no known endpoint» на
// seed после рестарта), и незачем — spoke сам пуллит hub. Раньше тут был тест
// HubReachesAll, который УТВЕРЖДАЛ багованное поведение и потому маскировал баг.
func TestGossipCandidatesHubDoesNotPickSpoke(t *testing.T) {
	// Мы = hub (с endpoint); spoke-a/spoke-b — NAT-spoke без endpoint.
	s := threeNodeState("HUB")
	got := GossipCandidates(s)
	if len(got) != 0 {
		t.Fatalf("hub must NOT gossip-poll NAT spokes (no endpoint), got %v", keysOf(got))
	}
}

// Два hub'а (оба с endpoint) опрашивают друг друга; NAT-spoke между ними отсекается.
func TestGossipCandidatesHubPicksOtherHub(t *testing.T) {
	s := &state.State{
		PublicKey: "HUB1",
		Peers: []state.Peer{
			{PublicKey: "HUB1", NodeIP: "100.64.0.1", Endpoint: "1.1.1.1:51820"},
			{PublicKey: "HUB2", NodeIP: "100.64.0.5", Endpoint: "2.2.2.2:51820"},
			{PublicKey: "SPOKE", NodeIP: "100.64.0.2"}, // NAT — отсечь
		},
	}
	got := keysOf(GossipCandidates(s))
	if !got["HUB2"] {
		t.Fatal("hub must gossip the other hub (it has an endpoint)")
	}
	if got["SPOKE"] {
		t.Fatal("hub must not gossip a NAT spoke")
	}
	if got["HUB1"] {
		t.Fatal("must never target self")
	}
}

// Spoke с двумя hub'ами видит оба.
func TestGossipCandidatesSpokePicksAllHubs(t *testing.T) {
	s := &state.State{
		PublicKey: "SPOKE",
		Peers: []state.Peer{
			{PublicKey: "SPOKE", NodeIP: "100.64.0.2"}, // мы, NAT
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

// Все за NAT (ни у кого нет endpoint) → 0 кандидатов везде. Это НАМЕРЕННО: такой
// mesh нефункционален by-design (никто никого не инициирует), gossip тут просто
// нечего делать. Тест фиксирует, что пустой результат — ожидание, а не баг.
func TestGossipCandidatesAllNATNoCandidates(t *testing.T) {
	s := &state.State{
		PublicKey: "A",
		Peers: []state.Peer{
			{PublicKey: "A", NodeIP: "100.64.0.2"}, // мы, NAT
			{PublicKey: "B", NodeIP: "100.64.0.3"}, // NAT
			{PublicKey: "C", NodeIP: "100.64.0.4"}, // NAT
		},
	}
	if got := GossipCandidates(s); len(got) != 0 {
		t.Fatalf("all-NAT mesh must yield 0 gossip candidates, got %v", keysOf(got))
	}
}

// Пир без NodeIP (некорректная/недорегистрированная запись) не кандидат.
func TestGossipCandidatesSkipsEmptyNodeIP(t *testing.T) {
	s := &state.State{
		PublicKey: "SPOKE",
		Peers: []state.Peer{
			{PublicKey: "SPOKE", NodeIP: "100.64.0.2"},
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
