package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileCreatesWithPerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want hello", got)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %v, want 0600", fi.Mode().Perm())
	}
}

func TestWriteFileOverwritesAndLeavesNoTmp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(path, []byte("OLD-LONGER"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WriteFile(path, []byte("NEW"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "NEW" {
		t.Fatalf("content = %q, want NEW", got)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("leftover .tmp after successful WriteFile")
	}
}
