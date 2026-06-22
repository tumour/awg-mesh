package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyAndReplaceBinary(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "new")
	target := filepath.Join(dir, "meshd")

	if err := os.WriteFile(src, []byte("NEWBIN"), 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}
	if err := os.WriteFile(target, []byte("OLDBIN"), 0o755); err != nil {
		t.Fatalf("write target: %v", err)
	}

	if err := ReplaceBinary(target, src, 0o755); err != nil {
		t.Fatalf("ReplaceBinary: %v", err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != "NEWBIN" {
		t.Fatalf("target not replaced, got %q", got)
	}
	fi, _ := os.Stat(target)
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("want mode 0755, got %v", fi.Mode().Perm())
	}
	// Промежуточный .new не должен оставаться.
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Fatal("leftover .new temp file after ReplaceBinary")
	}
}
