package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/tumour/awg-mesh/internal/state"
)

func TestPickHealthPeerPrefersSeed(t *testing.T) {
	s := &state.State{
		NodeIP: "100.64.0.5",
		Peers: []state.Peer{
			{NodeIP: "100.64.0.5"}, // мы сами — пропустить
			{NodeIP: "100.64.0.9"}, // обычный пир — fallback
			{NodeIP: "100.64.0.1", IsSeed: true},
		},
	}
	if got := pickHealthPeer(s); got != "100.64.0.1" {
		t.Fatalf("want seed 100.64.0.1, got %q", got)
	}
}

func TestPickHealthPeerFallbackToAnyPeer(t *testing.T) {
	s := &state.State{
		NodeIP: "100.64.0.5",
		Peers: []state.Peer{
			{NodeIP: "100.64.0.5"}, // мы сами
			{NodeIP: "100.64.0.9"}, // единственный сосед
		},
	}
	if got := pickHealthPeer(s); got != "100.64.0.9" {
		t.Fatalf("want fallback 100.64.0.9, got %q", got)
	}
}

func TestPickHealthPeerAloneInMesh(t *testing.T) {
	s := &state.State{
		NodeIP: "100.64.0.1",
		Peers:  []state.Peer{{NodeIP: "100.64.0.1", IsSeed: true}},
	}
	if got := pickHealthPeer(s); got != "" {
		t.Fatalf("want empty (alone), got %q", got)
	}
}

func TestPickHealthPeerSkipsEmptyIP(t *testing.T) {
	// Пир без NodeIP (NAT-нода ещё без выделенного IP — теоретически) пропускается.
	s := &state.State{
		NodeIP: "100.64.0.5",
		Peers: []state.Peer{
			{NodeIP: "", IsSeed: true}, // нет IP — не годится для probe
			{NodeIP: "100.64.0.9"},
		},
	}
	if got := pickHealthPeer(s); got != "100.64.0.9" {
		t.Fatalf("want 100.64.0.9 (skip empty-IP seed), got %q", got)
	}
}

// mustListen открывает loopback-listener на свободном порту (ошибка = fatal,
// чтобы не словить nil-deref на nil-listener'е).
func mustListen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return ln
}

// acceptAndClose обслуживает соединения (принять-и-закрыть) до закрытия ln.
func acceptAndClose(ln net.Listener) {
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
}

func TestDialReachableConnected(t *testing.T) {
	ln := mustListen(t)
	defer ln.Close()
	acceptAndClose(ln)

	ok, refused := dialReachable(ln.Addr().String())
	if !ok || refused {
		t.Fatalf("want ok=true refused=false, got ok=%v refused=%v", ok, refused)
	}
}

func TestDialReachableRefused(t *testing.T) {
	// Берём порт и сразу закрываем listener — порт почти наверняка свободен,
	// коннект получит RST (connection refused) на loopback.
	ln := mustListen(t)
	addr := ln.Addr().String()
	ln.Close()

	ok, refused := dialReachable(addr)
	if ok {
		t.Fatalf("want ok=false on closed port, got ok=true")
	}
	if !refused {
		t.Fatalf("want refused=true (RST) on closed loopback port, got refused=false")
	}
}

func TestMeshReachableSelfUpPeerRefused(t *testing.T) {
	// Локальный демон жив (слушаем как gossip) → здоров.
	selfLn := mustListen(t)
	defer selfLn.Close()
	acceptAndClose(selfLn)

	peerLn := mustListen(t)
	peerAddr := peerLn.Addr().String()
	peerLn.Close() // закрыт → RST

	if !meshReachable(selfLn.Addr().String(), peerAddr) {
		t.Fatal("want healthy: self up")
	}
}

func TestMeshReachableSelfUpSkipsUnreachablePeer(t *testing.T) {
	// OR-семантика (регресс на ложный откат): живой локальный демон = здоров,
	// даже если сосед недостижим (192.0.2.0/24 TEST-NET-1 не маршрутизируется).
	// self проверяется первым, поэтому до медленного peer-dial дело не доходит.
	selfLn := mustListen(t)
	defer selfLn.Close()
	acceptAndClose(selfLn)

	if !meshReachable(selfLn.Addr().String(), "192.0.2.1:9100") {
		t.Fatal("self up must be healthy regardless of an unreachable peer")
	}
}

func TestMeshReachableLocalDaemonDown(t *testing.T) {
	// Свой демон не слушает, соседа нет → нездорово.
	selfLn := mustListen(t)
	selfAddr := selfLn.Addr().String()
	selfLn.Close()

	if meshReachable(selfAddr, "") {
		t.Fatal("want unhealthy when local daemon is down and no peer")
	}
}

func TestMeshReachableBothEmpty(t *testing.T) {
	// Нечего проверять → недостижимо (не объявляем здоровым вслепую).
	if meshReachable("", "") {
		t.Fatal("want false when there is nothing to probe")
	}
}

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

	if err := replaceBinary(target, src, 0o755); err != nil {
		t.Fatalf("replaceBinary: %v", err)
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
	if fileExists(target + ".new") {
		t.Fatal("leftover .new temp file after replaceBinary")
	}
}
