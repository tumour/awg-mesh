package mesh

import (
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// BuildHealth — чистый маппер state → HealthView. Проверяем, что поля берутся
// из state как есть, а PendingFlagDay/PeersTotal считаются, а не хардкодятся.

func TestBuildHealthSeedWithPending(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	s := &state.State{
		NodeIP:        "100.64.0.1",
		IsSeed:        true,
		ParamsVersion: 7,
		Pending:       &state.PendingParams{Version: 8},
		Peers: []state.Peer{
			{PublicKey: "self", NodeIP: "100.64.0.1"},
			{PublicKey: "gw", NodeIP: "100.64.0.2"},
		},
		UpdatedAt: now,
	}

	h := BuildHealth(s)

	if h.NodeIP != "100.64.0.1" {
		t.Errorf("NodeIP = %q, want 100.64.0.1", h.NodeIP)
	}
	if !h.IsSeed {
		t.Error("IsSeed = false, want true")
	}
	if h.ParamsVersion != 7 {
		t.Errorf("ParamsVersion = %d, want 7", h.ParamsVersion)
	}
	if h.PeersTotal != 2 {
		t.Errorf("PeersTotal = %d, want 2", h.PeersTotal)
	}
	if !h.PendingFlagDay {
		t.Error("PendingFlagDay = false, want true (Pending задан)")
	}
	if !h.UpdatedAt.Equal(now) {
		t.Errorf("UpdatedAt = %v, want %v", h.UpdatedAt, now)
	}
}

func TestBuildHealthRegularNoPending(t *testing.T) {
	s := &state.State{
		NodeIP: "100.64.0.5",
		IsSeed: false,
		Peers:  nil, // нода без известных пиров
	}

	h := BuildHealth(s)

	if h.IsSeed {
		t.Error("IsSeed = true, want false для regular-ноды")
	}
	if h.PendingFlagDay {
		t.Error("PendingFlagDay = true при nil Pending, want false")
	}
	if h.PeersTotal != 0 {
		t.Errorf("PeersTotal = %d при nil Peers, want 0", h.PeersTotal)
	}
}
