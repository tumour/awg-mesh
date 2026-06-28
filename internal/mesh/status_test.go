package mesh

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

func TestBuildStatusMarksSelfAndRole(t *testing.T) {
	s := &state.State{
		NodeLabel:   "node-a",
		NetworkCIDR: "100.64.0.0/24",
		NodeIP:      "100.64.0.3",
		PublicKey:   "MYPUB",
		IsSeed:      false,
		Peers: []state.Peer{
			{Label: "hub", NodeIP: "100.64.0.1", Endpoint: "1.2.3.4:51820", PublicKey: "SEED", IsSeed: true},
			{Label: "node-a", NodeIP: "100.64.0.3", PublicKey: "MYPUB"},
		},
	}

	v := BuildStatus(s)
	if v.Role != "regular" {
		t.Fatalf("want role regular, got %s", v.Role)
	}
	if len(v.Peers) != 2 {
		t.Fatalf("want 2 peers, got %d", len(v.Peers))
	}
	// self помечен по совпадению pubkey
	var selfMarked, seedMarked int
	for _, p := range v.Peers {
		if p.IsSelf {
			selfMarked++
			if p.PublicKey != "MYPUB" {
				t.Fatalf("wrong peer marked self: %+v", p)
			}
		}
		if p.IsSeed {
			seedMarked++
		}
	}
	if selfMarked != 1 || seedMarked != 1 {
		t.Fatalf("want exactly 1 self and 1 seed, got self=%d seed=%d", selfMarked, seedMarked)
	}
}

func TestBuildStatusSeedRole(t *testing.T) {
	v := BuildStatus(&state.State{IsSeed: true})
	if v.Role != "seed" {
		t.Fatalf("want role seed, got %s", v.Role)
	}
}

// StatusView не должен содержать секретов: проверяем, что в JSON нет полей
// приватного ключа / cluster-secret (безопасность --json / web-API).
func TestStatusViewJSONHasNoSecrets(t *testing.T) {
	s := &state.State{
		NodeLabel: "n", PublicKey: "PUB", PrivateKey: "PRIVATE-SECRET",
		ClusterSecret: "CLUSTER-SECRET", NodeIP: "100.64.0.1",
	}
	b, err := json.Marshal(BuildStatus(s))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(b)
	if strings.Contains(out, "PRIVATE-SECRET") || strings.Contains(out, "CLUSTER-SECRET") {
		t.Fatalf("StatusView JSON leaks a secret: %s", out)
	}
}
