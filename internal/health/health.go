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

// SelfUp — поднялся ли НАШ демон: gossip-сокет на нашем mesh-IP принимает коннект.
// Сокет слушает только после того, как meshd поднял и сконфигурировал awg0, так
// что успешный коннект значит «свой демон жив». Пустой addr → false (ненастроенная
// нода — судить не о чем).
func SelfUp(selfAddr string) bool {
	if selfAddr == "" {
		return false
	}
	ok, _ := dialReachable(selfAddr)
	return ok
}

// PeerUp — достижим ли сосед ЧЕРЕЗ туннель: коннект ЛИБО RST-refused (хост ответил)
// значит, что awg0 реально маршрутизирует до него. Это сильный сигнал — он
// подтверждает живой data plane, а не только поднятый локальный сокет. Пустой
// addr → false (соседа нет).
func PeerUp(peerAddr string) bool {
	if peerAddr == "" {
		return false
	}
	ok, refused := dialReachable(peerAddr)
	return ok || refused
}

// UpgradeHealthy — решение watchdog'а «оставить только что применённый апгрейд или
// откатить», как чистая функция от трёх сигналов:
//
//	selfOK:       поднялся ли наш демон (SelfUp ПОСЛЕ апгрейда);
//	peerOK:       достижим ли сосед через туннель (PeerUp ПОСЛЕ апгрейда);
//	peerBaseline: был ли сосед достижим ДО апгрейда (peer-gate включается, только
//	              если было что терять).
//
// Логика: `peerOK || (selfOK && !peerBaseline)`.
//   - Живой туннель к соседу (peerOK) — всегда здоров: data plane маршрутизирует.
//   - Если туннель БЫЛ (peerBaseline), требуем его возврата. «Свой демон поднялся»
//     тут НЕ засчитывается: демон может забиндить сокет, не маршрутизируя ничего
//     (кривой бинарь/обфускация/peers) — это и есть self-first дыра, из-за которой
//     нода без out-of-band могла окирпичиться (новый бинарь «здоров», бэкап стёрт,
//     а связи нет). Лучше ложный откат на заведомо рабочий бинарь, чем кирпич.
//   - Если туннеля и не было (нода одна / уже изолирована до апгрейда), peer-gate
//     невозможен — падаем на selfOK как на лучшее доступное суждение, чтобы НЕ
//     откатывать здоровый апгрейд из-за заведомо отсутствующего соседа.
//
// Известный компромисс: если baseline-сосед (обычно seed) ляжет по своим причинам
// ровно в окно grace+timeout, здоровый апгрейд откатится. Для стабильного hub'а в
// окне ~минуты маловероятно, а асимметрия исходов намеренная: ложный откат
// восстановим, ложное «оставить» на ноде без out-of-band — нет.
func UpgradeHealthy(selfOK, peerOK, peerBaseline bool) bool {
	return peerOK || (selfOK && !peerBaseline)
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
