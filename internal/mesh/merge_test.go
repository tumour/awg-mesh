package mesh

import (
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

const (
	selfKey  = "SELF-KEY"
	testCIDR = "100.64.0.0/24"
)

func TestMergeAddsNewPeerAndFiltersSelf(t *testing.T) {
	local := []state.Peer{{Label: "me", PublicKey: selfKey, NodeIP: "100.64.0.2"}}
	remote := []state.Peer{
		{Label: "me", PublicKey: selfKey, NodeIP: "100.64.0.2"}, // мы сами — игнор
		{Label: "new", PublicKey: "NEW", Endpoint: "1.2.3.4:51820", NodeIP: "100.64.0.3"},
	}

	merged, changed, rejected := MergePeers(local, remote, selfKey, testCIDR)
	if len(rejected) != 0 {
		t.Fatalf("no rejections expected, got %v", rejected)
	}
	if len(merged) != 2 {
		t.Fatalf("want 2 merged peers (self + new), got %d: %+v", len(merged), merged)
	}
	if len(changed) != 1 || changed[0].PublicKey != "NEW" {
		t.Fatalf("want exactly NEW in changed, got %+v", changed)
	}
	if changed[0].Endpoint != "1.2.3.4:51820" {
		t.Fatalf("endpoint not propagated: %+v", changed[0])
	}
	if changed[0].LastSeen.IsZero() {
		t.Fatal("LastSeen not set on new peer")
	}
}

func TestMergeEndpointUpdatePushedToDevice(t *testing.T) {
	local := []state.Peer{
		{PublicKey: selfKey},
		{Label: "b", PublicKey: "B", Endpoint: "old.host:1", NodeIP: "100.64.0.3"},
	}
	remote := []state.Peer{
		{Label: "b", PublicKey: "B", Endpoint: "new.host:2", NodeIP: "100.64.0.3"},
	}

	merged, changed, _ := MergePeers(local, remote, selfKey, testCIDR)
	if len(changed) != 1 || changed[0].Endpoint != "new.host:2" {
		t.Fatalf("endpoint change must land in changed, got %+v", changed)
	}
	for _, p := range merged {
		if p.PublicKey == "B" && p.Endpoint != "new.host:2" {
			t.Fatalf("merged peer keeps stale endpoint: %+v", p)
		}
	}
}

func TestMergeEmptyRemoteEndpointDoesNotErase(t *testing.T) {
	local := []state.Peer{
		{Label: "b", PublicKey: "B", Endpoint: "known.host:51820", NodeIP: "100.64.0.3"},
	}
	remote := []state.Peer{
		{Label: "b", PublicKey: "B", Endpoint: "", NodeIP: "100.64.0.3"},
	}

	merged, changed, _ := MergePeers(local, remote, selfKey, testCIDR)
	if len(changed) != 0 {
		t.Fatalf("nothing to push, got %+v", changed)
	}
	if merged[0].Endpoint != "known.host:51820" {
		t.Fatalf("empty remote endpoint erased local: %+v", merged[0])
	}
	if merged[0].LastSeen.IsZero() {
		t.Fatal("LastSeen must refresh for peer confirmed by remote")
	}
}

func TestMergeLabelChangeNotPushedToDevice(t *testing.T) {
	local := []state.Peer{
		{Label: "old-name", PublicKey: "B", Endpoint: "h:1", NodeIP: "100.64.0.3"},
	}
	remote := []state.Peer{
		{Label: "new-name", PublicKey: "B", Endpoint: "h:1", NodeIP: "100.64.0.3"},
	}

	merged, changed, _ := MergePeers(local, remote, selfKey, testCIDR)
	if merged[0].Label != "new-name" {
		t.Fatalf("label not merged: %+v", merged[0])
	}
	if len(changed) != 0 {
		t.Fatalf("label-only change must not be pushed, got %+v", changed)
	}
}

func TestMergeDeduplicatesRemoteDuplicates(t *testing.T) {
	local := []state.Peer{{PublicKey: selfKey}}
	// remote с дублем pubkey "NEW" — должен добавиться ровно один раз.
	remote := []state.Peer{
		{Label: "new", PublicKey: "NEW", NodeIP: "100.64.0.3", Endpoint: "h:1"},
		{Label: "new-dup", PublicKey: "NEW", NodeIP: "100.64.0.3", Endpoint: "h:1"},
	}
	merged, changed, _ := MergePeers(local, remote, selfKey, testCIDR)
	cnt := 0
	for _, p := range merged {
		if p.PublicKey == "NEW" {
			cnt++
		}
	}
	if cnt != 1 {
		t.Fatalf("duplicate pubkey in remote must be added once, got %d", cnt)
	}
	if len(changed) != 1 {
		t.Fatalf("want 1 changed, got %d", len(changed))
	}
}

func TestMergeKeepsLocalUnknownToRemote(t *testing.T) {
	local := []state.Peer{
		{Label: "c", PublicKey: "C", NodeIP: "100.64.0.4"},
	}
	remote := []state.Peer{} // remote про C не знает

	merged, changed, _ := MergePeers(local, remote, selfKey, testCIDR)
	if len(merged) != 1 || merged[0].PublicKey != "C" {
		t.Fatalf("local peer dropped: %+v", merged)
	}
	if !merged[0].LastSeen.IsZero() {
		t.Fatal("LastSeen refreshed though remote did not confirm the peer")
	}
	if len(changed) != 0 {
		t.Fatalf("nothing changed, got %+v", changed)
	}
}

// --- security: одна нода не должна угнать чужой mesh-IP/маршрут через gossip ---

func TestMergeRejectsIPHijack(t *testing.T) {
	local := []state.Peer{
		{PublicKey: selfKey, NodeIP: "100.64.0.1"},
		{Label: "b", PublicKey: "B", NodeIP: "100.64.0.3", Endpoint: "b.host:51820"},
	}
	// Злая нода анонсирует НОВЫЙ pubkey на УЖЕ ЗАНЯТОМ IP B → попытка угона /32.
	remote := []state.Peer{
		{Label: "evil", PublicKey: "EVIL", NodeIP: "100.64.0.3", Endpoint: "evil.host:51820"},
	}
	merged, changed, rejected := MergePeers(local, remote, selfKey, testCIDR)
	for _, p := range merged {
		if p.PublicKey == "EVIL" {
			t.Fatal("EVIL peer hijacking 100.64.0.3 must not be merged")
		}
	}
	if len(changed) != 0 {
		t.Fatalf("hijack must not be pushed to device, got %+v", changed)
	}
	if len(rejected) != 1 {
		t.Fatalf("want 1 rejection reason, got %v", rejected)
	}
}

func TestMergeRejectsOutOfCIDR(t *testing.T) {
	local := []state.Peer{{PublicKey: selfKey, NodeIP: "100.64.0.1"}}
	// node_ip вне mesh-CIDR → попытка угнать маршрут к внешнему адресу (8.8.8.8).
	remote := []state.Peer{{Label: "x", PublicKey: "X", NodeIP: "8.8.8.8", Endpoint: "x.host:1"}}

	merged, changed, rejected := MergePeers(local, remote, selfKey, testCIDR)
	if len(changed) != 0 || len(rejected) != 1 {
		t.Fatalf("out-of-cidr peer must be rejected: changed=%+v rejected=%v", changed, rejected)
	}
	for _, p := range merged {
		if p.PublicKey == "X" {
			t.Fatal("out-of-cidr peer must not be merged")
		}
	}
}

func TestMergeRejectsInvalidNodeIP(t *testing.T) {
	local := []state.Peer{{PublicKey: selfKey, NodeIP: "100.64.0.1"}}
	remote := []state.Peer{
		{Label: "noip", PublicKey: "N", NodeIP: ""},
		{Label: "bad", PublicKey: "M", NodeIP: "not-an-ip"},
	}
	merged, changed, rejected := MergePeers(local, remote, selfKey, testCIDR)
	if len(changed) != 0 {
		t.Fatalf("invalid-IP peers must not be pushed, got %+v", changed)
	}
	if len(rejected) != 2 {
		t.Fatalf("want 2 rejections, got %v", rejected)
	}
	if len(merged) != 1 { // только self
		t.Fatalf("only self should remain, got %+v", merged)
	}
}

func TestMergeNewPeerInvalidEndpointNulled(t *testing.T) {
	local := []state.Peer{{PublicKey: selfKey, NodeIP: "100.64.0.1"}}
	// Новый peer с валидным IP, но кривым непустым endpoint → endpoint зануляется,
	// сам peer добавляется initiator-only (в state/device мусор не попадает).
	remote := []state.Peer{
		{Label: "n", PublicKey: "N", NodeIP: "100.64.0.7", Endpoint: "garbage-no-port"},
	}
	merged, changed, rejected := MergePeers(local, remote, selfKey, testCIDR)
	if len(changed) != 1 || changed[0].PublicKey != "N" {
		t.Fatalf("peer with valid IP must still be added, got changed=%+v", changed)
	}
	if changed[0].Endpoint != "" {
		t.Fatalf("invalid endpoint must be nulled, got %q", changed[0].Endpoint)
	}
	if len(rejected) != 1 {
		t.Fatalf("want 1 rejection note, got %v", rejected)
	}
	for _, p := range merged {
		if p.PublicKey == "N" && p.Endpoint != "" {
			t.Fatalf("merged peer keeps garbage endpoint: %+v", p)
		}
	}
}

func TestMergeRejectsInvalidEndpointFormat(t *testing.T) {
	local := []state.Peer{
		{Label: "b", PublicKey: "B", NodeIP: "100.64.0.3", Endpoint: "good.host:51820"},
	}
	// Существующему B пытаются подсунуть endpoint без порта → не применяем, старый
	// (рабочий) endpoint сохраняем и в device мусор не пушим.
	remote := []state.Peer{
		{Label: "b", PublicKey: "B", NodeIP: "100.64.0.3", Endpoint: "no-port-here"},
	}
	merged, changed, rejected := MergePeers(local, remote, selfKey, testCIDR)
	if len(changed) != 0 {
		t.Fatalf("garbage endpoint must not reach device, got %+v", changed)
	}
	if merged[0].Endpoint != "good.host:51820" {
		t.Fatalf("garbage endpoint must not overwrite good one: %+v", merged[0])
	}
	if len(rejected) != 1 {
		t.Fatalf("want 1 rejection, got %v", rejected)
	}
}
