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

func TestSelfUp(t *testing.T) {
	// Свой gossip-сокет слушает → демон поднял awg0 и жив.
	ln := mustListen(t)
	defer ln.Close()
	acceptAndClose(ln)
	if !SelfUp(ln.Addr().String()) {
		t.Fatal("want SelfUp=true while our gossip socket accepts")
	}

	// Сокет закрыт → демон не поднялся.
	down := mustListen(t)
	downAddr := down.Addr().String()
	down.Close()
	if SelfUp(downAddr) {
		t.Fatal("want SelfUp=false when our gossip socket is down")
	}

	// Пустой addr — нечего пробовать.
	if SelfUp("") {
		t.Fatal("want SelfUp=false for empty addr")
	}
}

func TestPeerUp(t *testing.T) {
	// Сосед слушает → коннект удался → туннель маршрутизирует.
	ln := mustListen(t)
	defer ln.Close()
	acceptAndClose(ln)
	if !PeerUp(ln.Addr().String()) {
		t.Fatal("want PeerUp=true while the peer accepts")
	}

	// Сосед достижим, но порт закрыт (RST): хост ответил → awg0 всё равно
	// маршрутизирует, считаем достижимым.
	rst := mustListen(t)
	rstAddr := rst.Addr().String()
	rst.Close()
	if !PeerUp(rstAddr) {
		t.Fatal("want PeerUp=true on RST (host reachable through the tunnel)")
	}

	// Пустой addr — соседа нет.
	if PeerUp("") {
		t.Fatal("want PeerUp=false for empty addr")
	}
}

// TestUpgradeHealthy — ядро peer-gated watchdog'а. Решение «оставить апгрейд или
// откатить» как чистая функция от трёх сигналов: жив ли свой демон, достижим ли
// сосед через туннель ПОСЛЕ апгрейда, и был ли сосед достижим ДО апгрейда.
//
// Суть фикса: если туннель БЫЛ (baseline=true), «свой демон поднялся» больше НЕ
// засчитывается как здоровье — демон может забиндить сокет, маршрутизируя ноль
// (self-first дыра, из-за которой нода без out-of-band могла окирпичиться).
// Требуем возврата туннеля.
func TestUpgradeHealthy(t *testing.T) {
	tests := []struct {
		name         string
		selfOK       bool
		peerOK       bool
		peerBaseline bool
		want         bool
	}{
		{"had tunnel, came back", true, true, true, true},
		// КЛЮЧЕВОЙ кейс: туннель был, демон встал, но туннель мёртв → ОТКАТ.
		{"had tunnel, daemon up but tunnel dead -> rollback", true, false, true, false},
		{"had tunnel, total death", false, false, true, false},
		// Туннеля не было (нода и так изолирована/одна) → лучшее, что можем —
		// убедиться, что свой демон вернулся; ложный откат тут не нужен.
		{"was isolated, daemon back -> best effort keep", true, false, false, true},
		{"was isolated, nothing", false, false, false, false},
		// Связь УЛУЧШИЛАСЬ (соседа не было, стал) — однозначно здорово.
		{"no baseline but peer now reachable", false, true, false, true},
		{"working tunnel always healthy", false, true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UpgradeHealthy(tt.selfOK, tt.peerOK, tt.peerBaseline)
			if got != tt.want {
				t.Fatalf("UpgradeHealthy(self=%v peer=%v baseline=%v) = %v, want %v",
					tt.selfOK, tt.peerOK, tt.peerBaseline, got, tt.want)
			}
		})
	}
}
