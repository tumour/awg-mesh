package node

import (
	"path/filepath"
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

func TestInitCreatesSeedState(t *testing.T) {
	sf := filepath.Join(t.TempDir(), "state.json")
	res, err := Init(InitParams{
		Label:          "seed",
		ListenPort:     51820,
		PublicEndpoint: "1.2.3.4:51820",
		CIDR:           "100.64.0.0/24",
		StateFile:      sf,
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if res.NodeIP != "100.64.0.1" {
		t.Fatalf("seed IP want .1, got %s", res.NodeIP)
	}
	if res.JoinToken == "" {
		t.Fatal("empty join token")
	}

	s, err := state.Load(sf)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !s.IsSeed || s.NodeIP != "100.64.0.1" || len(s.Peers) != 1 {
		t.Fatalf("bad seed state: %+v", s)
	}
	if s.Version != state.CurrentVersion {
		t.Fatalf("Save must set CurrentVersion, got %d", s.Version)
	}
}

func TestInitRequiresLabelAndEndpoint(t *testing.T) {
	dir := t.TempDir()
	if _, err := Init(InitParams{StateFile: filepath.Join(dir, "a.json"), PublicEndpoint: "1.2.3.4:1", CIDR: "100.64.0.0/24"}); err == nil {
		t.Fatal("want error without label")
	}
	if _, err := Init(InitParams{StateFile: filepath.Join(dir, "b.json"), Label: "x", CIDR: "100.64.0.0/24"}); err == nil {
		t.Fatal("want error without public-endpoint")
	}
}

func TestInitRefusesExistingState(t *testing.T) {
	sf := filepath.Join(t.TempDir(), "state.json")
	p := InitParams{Label: "s", ListenPort: 1, PublicEndpoint: "1.2.3.4:1", CIDR: "100.64.0.0/24", StateFile: sf}
	if _, err := Init(p); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if _, err := Init(p); err == nil {
		t.Fatal("second init must refuse existing state")
	}
}
