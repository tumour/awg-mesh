package state

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	s := &State{Version: 1, NodeLabel: "test"}
	if err := s.Save(path); err != nil {
		t.Fatalf("save initial state: %v", err)
	}
	return NewStore(path), path
}

// Регрессионный тест на lost update: конкурентные Update'ы (как bootstrap-join
// параллельно с gossip-merge) не должны терять записи.
func TestStoreConcurrentUpdates(t *testing.T) {
	store, _ := newTestStore(t)

	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := store.Update(func(s *State) error {
				s.Peers = append(s.Peers, Peer{
					Label:     fmt.Sprintf("peer-%d", i),
					PublicKey: fmt.Sprintf("key-%d", i),
				})
				return nil
			})
			if err != nil {
				t.Errorf("update %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	s, err := store.Read()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(s.Peers) != n {
		t.Fatalf("lost updates: want %d peers, got %d", n, len(s.Peers))
	}
}

func TestStoreUpdateNoChangeSkipsWrite(t *testing.T) {
	store, path := newTestStore(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	s, err := store.Update(func(*State) error { return ErrNoChange })
	if err != nil {
		t.Fatalf("ErrNoChange must not surface as error, got: %v", err)
	}
	if s == nil {
		t.Fatal("want state snapshot, got nil")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("file rewritten despite ErrNoChange")
	}
}

func TestStoreUpdateErrorAbortsWrite(t *testing.T) {
	store, path := newTestStore(t)
	before, _ := os.ReadFile(path)

	boom := errors.New("boom")
	if _, err := store.Update(func(s *State) error {
		s.NodeLabel = "mutated"
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("want boom, got %v", err)
	}

	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("file rewritten despite fn error")
	}
}
