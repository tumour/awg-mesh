package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
)

// TestSaveLoadV2RoundTrip — v2-state (AWG-2.0: H-диапазоны, S3/S4, LocalObf)
// сохраняется и читается без потерь.
func TestSaveLoadV2RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	in := &State{
		NodeLabel:     "hetzner",
		ClusterSecret: "secret",
		AwgParams: awgparams.Params{
			Jc: 5, Jmin: 50, Jmax: 200, S1: 30, S2: 41, S3: 32, S4: 16,
			H1: awgparams.HeaderRange{Min: 100, Max: 200},
			H2: awgparams.HeaderRange{Min: 300, Max: 400},
			H3: awgparams.HeaderRange{Min: 500, Max: 600},
			H4: awgparams.HeaderRange{Min: 700, Max: 800},
		},
		LocalObf:    awgparams.LocalObf{I1: "<r 64>"},
		NetworkCIDR: "100.64.0.0/24",
		NodeIP:      "100.64.0.4",
		IsSeed:      false,
		// flag-day: версия + Pending (с вложенными Params/HeaderRange) обязаны
		// пережить сериализацию — иначе gossip раздаст нечитаемое.
		ParamsVersion: 5,
		Pending: &PendingParams{
			Version: 6,
			ApplyAt: time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC),
			Params: awgparams.Params{
				Jc: 4, Jmin: 50, Jmax: 200, S1: 30, S2: 41, S3: 30, S4: 20,
				H1: awgparams.HeaderRange{Min: 11, Max: 22},
				H2: awgparams.HeaderRange{Min: 33, Max: 44},
				H3: awgparams.HeaderRange{Min: 55, Max: 66},
				H4: awgparams.HeaderRange{Min: 77, Max: 88},
			},
		},
	}
	if err := in.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if out.Version != CurrentVersion {
		t.Errorf("version = %d, want %d", out.Version, CurrentVersion)
	}
	if out.AwgParams != in.AwgParams {
		t.Errorf("AwgParams mismatch:\n got %+v\nwant %+v", out.AwgParams, in.AwgParams)
	}
	if out.LocalObf != in.LocalObf {
		t.Errorf("LocalObf mismatch: got %+v want %+v", out.LocalObf, in.LocalObf)
	}
	if out.ParamsVersion != 5 {
		t.Errorf("ParamsVersion = %d, want 5", out.ParamsVersion)
	}
	if out.Pending == nil {
		t.Fatal("Pending потерян при round-trip")
	}
	if out.Pending.Version != in.Pending.Version || !out.Pending.ApplyAt.Equal(in.Pending.ApplyAt) ||
		out.Pending.Params != in.Pending.Params {
		t.Errorf("Pending mismatch:\n got %+v\nwant %+v", out.Pending, in.Pending)
	}
}

// TestLoadMigratesV1 — старый v1-state (H как число, без s3/s4/local_obf)
// дочитывается БЕСШУМНО: H→{H,H}, s3/s4=0, local_obf пуст. Это разблокирует
// self-upgrade без re-init. Identity/peers сохраняются.
func TestLoadMigratesV1(t *testing.T) {
	const v1 = `{
  "version": 1,
  "node_label": "ax3200",
  "cluster_secret": "AAAA",
  "awg_params": {"jc": 5, "jmin": 20, "jmax": 100, "s1": 30, "s2": 40,
                 "h1": 123456, "h2": 234567, "h3": 345678, "h4": 456789},
  "network_cidr": "100.64.0.0/24",
  "private_key": "cHJpdg==", "public_key": "cHViMQ==",
  "node_ip": "100.64.0.2", "listen_port": 0, "is_seed": false,
  "peers": [{"label": "vps", "public_key": "cGVlcg==", "node_ip": "100.64.0.1"}]
}`
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(v1), 0600); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	s, err := Load(path)
	if err != nil {
		t.Fatalf("load v1 (should migrate silently): %v", err)
	}
	// H число → вырожденный диапазон {n,n} (wire-identical старому).
	if s.AwgParams.H1 != (awgparams.HeaderRange{Min: 123456, Max: 123456}) {
		t.Errorf("H1 = %+v, want {123456,123456}", s.AwgParams.H1)
	}
	if s.AwgParams.H4 != (awgparams.HeaderRange{Min: 456789, Max: 456789}) {
		t.Errorf("H4 = %+v, want {456789,456789}", s.AwgParams.H4)
	}
	// Новые поля дефолтятся: s3/s4=0 (паддинг выкл), local_obf пуст.
	if s.AwgParams.S3 != 0 || s.AwgParams.S4 != 0 {
		t.Errorf("s3/s4 = %d/%d, want 0/0 (wire-identical)", s.AwgParams.S3, s.AwgParams.S4)
	}
	if s.LocalObf != (awgparams.LocalObf{}) {
		t.Errorf("local_obf = %+v, want empty", s.LocalObf)
	}
	// Старые поля и identity сохранены.
	if s.AwgParams.Jc != 5 || s.AwgParams.S1 != 30 || s.NodeIP != "100.64.0.2" {
		t.Errorf("v1 data lost: jc=%d s1=%d ip=%s", s.AwgParams.Jc, s.AwgParams.S1, s.NodeIP)
	}
	if len(s.Peers) != 1 || s.Peers[0].Label != "vps" {
		t.Errorf("peers lost: %+v", s.Peers)
	}

	// После Save файл перезаписывается в v2 и читается уже как v2.
	if err := s.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	s2, err := Load(path)
	if err != nil {
		t.Fatalf("reload v2: %v", err)
	}
	if s2.Version != CurrentVersion || s2.AwgParams.H1.Min != 123456 {
		t.Errorf("after Save not v2: version=%d h1=%+v", s2.Version, s2.AwgParams.H1)
	}
}

// TestLoadRejectsFutureVersion — state новее бинаря реджектится (читать опасно).
func TestLoadRejectsFutureVersion(t *testing.T) {
	future := `{"version":999,"node_label":"x"}`
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(future), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error loading future-version state, got nil")
	}
}
