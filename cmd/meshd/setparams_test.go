package main

import (
	"path/filepath"
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

func TestCmdSetParams(t *testing.T) {
	dir := t.TempDir()

	t.Run("seed анонсирует flag-day (Pending версии+1, ApplyAt не назначен)", func(t *testing.T) {
		sf := filepath.Join(dir, "seed.json")
		if err := (&state.State{NodeLabel: "s", IsSeed: true, ParamsVersion: 2}).Save(sf); err != nil {
			t.Fatal(err)
		}
		if err := cmdSetParams([]string{"--state-file", sf}); err != nil {
			t.Fatalf("cmdSetParams on seed: %v", err)
		}
		s, err := state.Load(sf)
		if err != nil {
			t.Fatal(err)
		}
		if s.Pending == nil {
			t.Fatal("Pending не установлен")
		}
		if s.Pending.Version != 3 {
			t.Errorf("Version = %d, want 3 (current+1)", s.Pending.Version)
		}
		if !s.Pending.ApplyAt.IsZero() {
			t.Errorf("ApplyAt = %v должен быть нулевым (announced, не закоммичен)", s.Pending.ApplyAt)
		}
	})

	t.Run("regular-нода отказывает", func(t *testing.T) {
		rf := filepath.Join(dir, "reg.json")
		if err := (&state.State{NodeLabel: "r", IsSeed: false}).Save(rf); err != nil {
			t.Fatal(err)
		}
		if err := cmdSetParams([]string{"--state-file", rf}); err == nil {
			t.Fatal("ожидалась ошибка: set-params не на seed")
		}
	})
}
