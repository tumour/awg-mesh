// Package health — сетевая проверка достижимости ноды по mesh через TCP-probe
// gossip-сокета. Чистая и без зависимости на gossip/upgrade-логику —
// переиспользуема (self-upgrade watchdog сейчас, live-статус позже).
package health

import (
	"net"
	"strings"
	"time"
)

// dialTimeout — таймаут одного TCP-probe.
const dialTimeout = 3 * time.Second

// Addr — host:port, либо "" если host пуст (сохраняем семантику «пропусти probe»).
func Addr(host, port string) string {
	if host == "" {
		return ""
	}
	return net.JoinHostPort(host, port)
}

// Reachable — здорова ли нода. Здорова, если ЛИБО свой демон поднялся, ЛИБО
// туннель до соседа жив:
//
//	selfAddr: наш gossip-сокет (слушает на mesh-IP только когда демон поднял и
//	          сконфигурировал awg0) — успешный коннект значит «свой демон жив».
//	peerAddr: gossip-сокет соседа. Достижимость через туннель (коннект ЛИБО
//	          RST-refused) значит «awg0 реально маршрутизирует».
//
// Семантика OR, а не AND: оффлайн-сосед (перезагружается по своим причинам) НЕ
// должен вызывать откат здорового апгрейда, если свой демон уже вернулся. Пустой
// addr пропускается; если оба пусты — судить не о чем, считаем недостижимым.
//
// selfAddr — основной сигнал. peer-only суждение (selfAddr пуст) слабее: в теории
// маршрут к соседу мог бы пережить мёртвый демон и дать ложный «healthy». Но awg0
// у нас userspace-TUN, привязанный к процессу meshd — со смертью демона интерфейс
// и его маршруты исчезают, так что на практике stale-route почти не случается. И
// selfAddr (== наш mesh-IP) задан почти всегда; пустой он лишь на ненастроенной ноде.
func Reachable(selfAddr, peerAddr string) bool {
	if selfAddr != "" {
		if ok, _ := dialReachable(selfAddr); ok {
			return true
		}
	}
	if peerAddr != "" {
		if ok, refused := dialReachable(peerAddr); ok || refused {
			return true
		}
	}
	return false
}

// dialReachable пробует TCP-коннект к addr. Возвращает:
//
//	ok=true      — соединение установлено (на той стороне слушают)
//	refused=true — пришёл RST: хост достижим, но порт закрыт
//	оба false    — таймаут / no route: хост недостижим
func dialReachable(addr string) (ok, refused bool) {
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err == nil {
		_ = conn.Close()
		return true, false
	}
	return false, strings.Contains(err.Error(), "connection refused")
}
