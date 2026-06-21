package mesh

import (
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

const selfKey = "SELF-KEY"

func TestMergeAddsNewPeerAndFiltersSelf(t *testing.T) {
	local := []state.Peer{{Label: "me", PublicKey: selfKey, NodeIP: "100.64.0.2"}}
	remote := []state.Peer{
		{Label: "me", PublicKey: selfKey, NodeIP: "100.64.0.2"}, // мы сами — игнор
		{Label: "new", PublicKey: "NEW", Endpoint: "1.2.3.4:51820", NodeIP: "100.64.0.3"},
	}

	merged, changed := MergePeers(local, remote, selfKey)
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

	merged, changed := MergePeers(local, remote, selfKey)
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

	merged, changed := MergePeers(local, remote, selfKey)
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

	merged, changed := MergePeers(local, remote, selfKey)
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
	merged, changed := MergePeers(local, remote, selfKey)
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

	merged, changed := MergePeers(local, remote, selfKey)
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
