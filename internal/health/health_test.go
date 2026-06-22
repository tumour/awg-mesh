package health

import (
	"net"
	"testing"
)

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

func TestReachableSelfUpPeerRefused(t *testing.T) {
	// Локальный демон жив (слушаем как gossip) → здоров.
	selfLn := mustListen(t)
	defer selfLn.Close()
	acceptAndClose(selfLn)

	peerLn := mustListen(t)
	peerAddr := peerLn.Addr().String()
	peerLn.Close() // закрыт → RST

	if !Reachable(selfLn.Addr().String(), peerAddr) {
		t.Fatal("want healthy: self up")
	}
}

func TestReachableSelfUpSkipsUnreachablePeer(t *testing.T) {
	// OR-семантика (регресс на ложный откат): живой локальный демон = здоров,
	// даже если сосед недостижим (192.0.2.0/24 TEST-NET-1 не маршрутизируется).
	// self проверяется первым, поэтому до медленного peer-dial дело не доходит.
	selfLn := mustListen(t)
	defer selfLn.Close()
	acceptAndClose(selfLn)

	if !Reachable(selfLn.Addr().String(), "192.0.2.1:9100") {
		t.Fatal("self up must be healthy regardless of an unreachable peer")
	}
}

func TestReachableLocalDaemonDown(t *testing.T) {
	// Свой демон не слушает, соседа нет → нездорово.
	selfLn := mustListen(t)
	selfAddr := selfLn.Addr().String()
	selfLn.Close()

	if Reachable(selfAddr, "") {
		t.Fatal("want unhealthy when local daemon is down and no peer")
	}
}

func TestReachableBothEmpty(t *testing.T) {
	// Нечего проверять → недостижимо (не объявляем здоровым вслепую).
	if Reachable("", "") {
		t.Fatal("want false when there is nothing to probe")
	}
}
