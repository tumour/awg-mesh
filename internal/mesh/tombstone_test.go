package mesh

import (
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

func tomb(pub string) state.Tombstone { return state.Tombstone{PublicKey: pub} }

func tombKeys(ts []state.Tombstone) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.PublicKey
	}
	return out
}

func peerKeys(ps []state.Peer) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.PublicKey
	}
	return out
}

func findKeptPeer(ps []state.Peer, pub string) *state.Peer {
	for i := range ps {
		if ps[i].PublicKey == pub {
			return &ps[i]
		}
	}
	return nil
}

func eqStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNewTombstone(t *testing.T) {
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	p := state.Peer{Label: "node-a", PublicKey: "ORPH", NodeIP: "100.64.0.4"}
	ts := NewTombstone(p, now)

	if ts.PublicKey != "ORPH" {
		t.Errorf("PublicKey = %q, want ORPH", ts.PublicKey)
	}
	if ts.Label != "node-a" {
		t.Errorf("Label = %q, want node-a (для аудита)", ts.Label)
	}
	if !ts.RevokedAt.Equal(now) {
		t.Errorf("RevokedAt = %v, want %v", ts.RevokedAt, now)
	}
	if !IsRevoked([]state.Tombstone{ts}, "ORPH") {
		t.Error("свежий tombstone должен делать pubkey отозванным")
	}
}

func TestIsRevoked(t *testing.T) {
	ts := []state.Tombstone{tomb("A"), tomb("B")}
	cases := []struct {
		pubkey string
		want   bool
	}{
		{"A", true},
		{"B", true},
		{"C", false},
		{"", false},
	}
	for _, c := range cases {
		if got := IsRevoked(ts, c.pubkey); got != c.want {
			t.Errorf("IsRevoked(%q) = %v, want %v", c.pubkey, got, c.want)
		}
	}
	if IsRevoked(nil, "A") {
		t.Error("пустой набор не отзывает никого")
	}
}

func TestMergeTombstones(t *testing.T) {
	cases := []struct {
		name       string
		local      []state.Tombstone
		remote     []state.Tombstone
		wantMerged []string
		wantAdded  []string
	}{
		{
			"remote пуст — merged = local, ничего не добавлено",
			[]state.Tombstone{tomb("A")}, nil,
			[]string{"A"}, nil,
		},
		{
			"local пуст — все remote новые",
			nil, []state.Tombstone{tomb("A"), tomb("B")},
			[]string{"A", "B"}, []string{"A", "B"},
		},
		{
			"повтор (remote ⊆ local) — идемпотентно, added пуст",
			[]state.Tombstone{tomb("A"), tomb("B")}, []state.Tombstone{tomb("A")},
			[]string{"A", "B"}, nil,
		},
		{
			"частичное пересечение — добавляется только новый, порядок local→remote",
			[]state.Tombstone{tomb("A")}, []state.Tombstone{tomb("A"), tomb("C")},
			[]string{"A", "C"}, []string{"C"},
		},
		{
			"дубли в local дедуплицируются",
			[]state.Tombstone{tomb("A"), tomb("A")}, nil,
			[]string{"A"}, nil,
		},
		{
			"дубли в remote дедуплицируются (added без повторов)",
			nil, []state.Tombstone{tomb("X"), tomb("X")},
			[]string{"X"}, []string{"X"},
		},
		{
			"пустой pubkey — мусор, НЕ оседает (иначе IsRevoked(\"\") и флуд пустыми)",
			nil, []state.Tombstone{tomb(""), tomb("A")},
			[]string{"A"}, []string{"A"},
		},
		{
			"пустой pubkey в local тоже отбрасывается",
			[]state.Tombstone{tomb(""), tomb("A")}, nil,
			[]string{"A"}, nil,
		},
	}
	for _, c := range cases {
		merged, added := MergeTombstones(c.local, c.remote)
		if !eqStr(tombKeys(merged), c.wantMerged) {
			t.Errorf("%s: merged = %v, want %v", c.name, tombKeys(merged), c.wantMerged)
		}
		if !eqStr(tombKeys(added), c.wantAdded) {
			t.Errorf("%s: added = %v, want %v", c.name, tombKeys(added), c.wantAdded)
		}
	}
}

func TestMergeTombstonesIdempotent(t *testing.T) {
	// Повторный merge собственного результата не меняет набор и не порождает added —
	// gossip может крутить tombstone по кругу бесконечно без эффекта.
	local := []state.Tombstone{tomb("A"), tomb("B")}
	merged, _ := MergeTombstones(local, []state.Tombstone{tomb("B"), tomb("A")})
	merged2, added2 := MergeTombstones(merged, merged)
	if !eqStr(tombKeys(merged2), []string{"A", "B"}) {
		t.Errorf("merged2 = %v, want [A B]", tombKeys(merged2))
	}
	if added2 != nil {
		t.Errorf("added2 = %v, want nil (идемпотентно)", tombKeys(added2))
	}
}

func TestApplyTombstones(t *testing.T) {
	peers := []state.Peer{
		{PublicKey: "seed", NodeIP: "100.64.0.1"},
		{PublicKey: "ORPH", NodeIP: "100.64.0.4"}, // осиротевшая запись
		{PublicKey: "node-b", NodeIP: "100.64.0.5"},
	}

	t.Run("пустой набор — всё kept, ничего removed", func(t *testing.T) {
		kept, removed := ApplyTombstones(peers, nil, "")
		if !eqStr(peerKeys(kept), []string{"seed", "ORPH", "node-b"}) {
			t.Errorf("kept = %v, want все", peerKeys(kept))
		}
		if removed != nil {
			t.Errorf("removed = %v, want nil", peerKeys(removed))
		}
	})

	t.Run("отзыв одного — он в removed, остальные в kept", func(t *testing.T) {
		kept, removed := ApplyTombstones(peers, []state.Tombstone{tomb("ORPH")}, "")
		if !eqStr(peerKeys(kept), []string{"seed", "node-b"}) {
			t.Errorf("kept = %v, want [seed node-b]", peerKeys(kept))
		}
		if !eqStr(peerKeys(removed), []string{"ORPH"}) {
			t.Errorf("removed = %v, want [ORPH]", peerKeys(removed))
		}
	})

	t.Run("tombstone без соответствующего peer — no-op (идемпотентно)", func(t *testing.T) {
		kept, removed := ApplyTombstones(peers, []state.Tombstone{tomb("gone")}, "")
		if !eqStr(peerKeys(kept), []string{"seed", "ORPH", "node-b"}) {
			t.Errorf("kept = %v, want все", peerKeys(kept))
		}
		if removed != nil {
			t.Errorf("removed = %v, want nil (peer уже отсутствует)", peerKeys(removed))
		}
	})

	// Security: self НЕ снимается даже под собственным tombstone (форж tombstone(self)
	// через gossip иначе заставил бы ноду удалить себя из своего же peer-list).
	t.Run("self под своим tombstone остаётся (spare-self)", func(t *testing.T) {
		kept, removed := ApplyTombstones(peers, []state.Tombstone{tomb("seed"), tomb("ORPH")}, "seed")
		for _, p := range removed {
			if p.PublicKey == "seed" {
				t.Fatal("self не должен попадать в removed под форженным tombstone")
			}
		}
		if findKeptPeer(kept, "seed") == nil {
			t.Fatal("self должен остаться в kept")
		}
		if findKeptPeer(kept, "ORPH") != nil {
			t.Fatal("ORPH (не self) должен быть снят")
		}
	})
}
