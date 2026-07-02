// Package mesh — доменное ядро mesh-сети: аллокация IP, merge peer-list'а,
// построение статуса. Платформо-НЕЗАВИСИМО (ни CLI, ни HTTP, ни ОС-вызовов),
// зависит только от internal/state. Единый источник доменной логики для всех
// фронтендов (CLI, --json, web-дашборд, LuCI) и control-plane (gossip, bootstrap).
package mesh

import (
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// MergePeers — мерж локального peer-list'а с peer-list'ом remote-ноды (gossip).
// Работает на доменной модели state.Peer (caller конвертирует свой wire-тип в
// state.Peer заранее — домен не знает про gossip/proto-форматы).
//
// Возвращает (merged, changed, rejected, persist):
//
//	merged   — полный новый список для state.Peers (обновлённые endpoint/label
//	           существующих peer'ов + refresh'нутые LastSeen). is_seed из gossip
//	           НЕ применяется (см. ниже — он неаутентифицирован).
//	changed  — что пушить в wg-device через UpdatePeer (новые peers + те, у кого
//	           сменился endpoint). Pure refresh LastSeen / label в changed
//	           не идёт — на wg-маршрутизацию они не влияют.
//	rejected — человекочитаемые причины отказа (для лога caller'ом).
//	persist  — надо ли писать merged на диск. Это ОТДЕЛЬНЫЙ вопрос от changed:
//	           label-обновления значимы для state, но не для wg-device, так
//	           что попадают в persist, но не в changed. Чистый refresh LastSeen в
//	           persist НЕ идёт сознательно — иначе писали бы файл каждый gossip-цикл
//	           (flash-wear на роутере); LastSeen долетит на диск с ближайшей реальной
//	           записью. Caller решает запись по persist, НЕ по len(changed).
//
// БЕЗОПАСНОСТЬ (trust-by-tunneling плоское: любая нода в mesh может прислать
// произвольный peer-list). Чтобы одна нода не угнала чужой mesh-IP/маршрут,
// нового peer'а отвергаем, если его NodeIP: невалиден, вне networkCIDR, или уже
// принадлежит ДРУГОМУ pubkey (коллизия → cryptokey-routing last-write-wins отдал
// бы /32 атакующему). NodeIP существующих peer'ов через gossip не меняется
// (матчим по pubkey), поэтому угон возможен только через «нового» peer'а.
//
// is_seed из gossip НЕ применяется вовсе (ни новому, ни существующему peer'у) —
// seed-статус аутентифицирован только bootstrap-каналом (join) и локальным init.
// Иначе самозванец объявил бы себя seed'ом и захватил flag-day-плоскость
// (seedAuthorized → push /v1/params, /v1/obf). См. проход по local ниже.
//
// Себя из remote всегда отфильтровываем (selfPub); себя из local сохраняем как есть.
//
// REVOKE (tombstones, см. tombstone.go): отозванные ноды исключаются из merge —
// и из local (выкидываем из merged, persist=true; на wg-device их снимет RemovePeer
// у caller'а), и из remote (НЕ воскрешаем union'ом). Это и есть перекрытие реанонса:
// без него удалить ноду нельзя — сосед вернул бы её следующим gossip-pull'ом.
//
// Честность LastSeen: refresh означает «remote-нода знает этого peer'а», а НЕ
// «peer жив» — прямого health-check'а нет. Не строить на этом expiry-логику.
//
// ОСТАЁТСЯ открытым (нужны подписи, v0.2): forged-but-valid endpoint существующего
// peer'а. MergePeers применяет смену endpoint'а от любого источника (last-pull-wins),
// поэтому злая нода может перенаправить трафик к соседу в никуда (DoS). Здесь
// валидируется только ФОРМАТ endpoint'а, не его подлинность.
func MergePeers(local, remote []state.Peer, tombstones []state.Tombstone, selfPub, networkCIDR string) (merged, changed []state.Peer, rejected []string, persist bool) {
	// Индекс remote по pubkey, заодно отфильтровываем себя.
	rByKey := make(map[string]state.Peer, len(remote))
	for _, r := range remote {
		if r.PublicKey == selfPub {
			continue
		}
		rByKey[r.PublicKey] = r
	}

	// Владельцы mesh-IP по локальному (доверенному) состоянию — для детекта угона.
	// Включаем себя: чужой pubkey, claim'ящий наш NodeIP, тоже коллизия.
	ipOwner := make(map[string]string, len(local))
	for _, p := range local {
		if p.NodeIP != "" {
			ipOwner[p.NodeIP] = p.PublicKey
		}
	}

	var cidr *net.IPNet
	if _, n, err := net.ParseCIDR(networkCIDR); err == nil {
		cidr = n // невалидный CIDR → membership-проверку пропускаем (fail-open)
	}

	now := time.Now().UTC()
	localKeys := make(map[string]bool, len(local))

	// Проход по local — обновляем endpoint/label/IsSeed если remote знает свежее.
	for _, p := range local {
		localKeys[p.PublicKey] = true
		if p.PublicKey == selfPub {
			merged = append(merged, p)
			continue
		}
		// Отозванного выкидываем из peer-list (на wg-device его снимет RemovePeer у
		// caller'а). localKeys помечен выше — значит как «новый» из remote он тоже не
		// вернётся. persist: merged изменился относительно local — надо записать диск.
		if IsRevoked(tombstones, p.PublicKey) {
			persist = true
			continue
		}
		r, ok := rByKey[p.PublicKey]
		if !ok {
			// Remote не знает — оставляем как есть, LastSeen не refresh'аем.
			merged = append(merged, p)
			continue
		}
		updated := p
		updated.LastSeen = now
		// Endpoint меняем только если прислан непустой, отличный и валидный по
		// формату (мусор не должен попасть ни в state, ни в UAPI wg-device).
		endpointChanged := r.Endpoint != "" && r.Endpoint != p.Endpoint
		if endpointChanged && !ValidEndpoint(r.Endpoint) {
			rejected = append(rejected, fmt.Sprintf("peer %s: invalid endpoint %q",
				ShortKey(p.PublicKey), r.Endpoint))
			endpointChanged = false
		}
		if endpointChanged {
			updated.Endpoint = r.Endpoint
		}
		labelChanged := r.Label != "" && r.Label != p.Label
		if labelChanged {
			updated.Label = r.Label
		}
		// is_seed СОЗНАТЕЛЬНО не берём из gossip: seed-статус узнаётся только из
		// bootstrap-response (join, Noise-аутентифицирован) и локального init. Иначе
		// любая нода объявила бы про себя is_seed=true в своём peer-list'е → сосед
		// смёрджил бы это → самозванец прошёл бы seedAuthorized (gossip/obf.go) и
		// пушил flag-day POST /v1/params (согласованный разрыв mesh) и /v1/obf.
		// `updated := p` сохраняет локальный IsSeed — и downgrade через gossip тоже
		// невозможен. Единственный seed известен каждой ноде с init/join.
		merged = append(merged, updated)
		// changed → пуш в wg-device (только endpoint-смена; label на wg не влияет).
		if endpointChanged {
			changed = append(changed, updated)
		}
		// persist → запись на диск: значимое изменение (endpoint/label), но НЕ чистый
		// LastSeen-refresh (иначе писали бы файл каждый цикл — flash-wear).
		if endpointChanged || labelChanged {
			persist = true
		}
	}

	// Новые peers — те что есть в remote, но не в local. seenNew дедуплицирует
	// дубликаты pubkey в самом remote-ответе, сохраняя порядок remote.
	seenNew := make(map[string]bool)
	for _, r := range remote {
		if r.PublicKey == selfPub || localKeys[r.PublicKey] || seenNew[r.PublicKey] {
			continue
		}
		// Перекрытие реанонса: отозванного соседа НЕ воскрешаем union'ом.
		if IsRevoked(tombstones, r.PublicKey) {
			rejected = append(rejected, fmt.Sprintf("peer %s: revoked, reanimation blocked", ShortKey(r.PublicKey)))
			continue
		}
		if reason := rejectNewPeer(r, ipOwner, cidr, networkCIDR); reason != "" {
			rejected = append(rejected, reason)
			continue
		}
		seenNew[r.PublicKey] = true
		ipOwner[r.NodeIP] = r.PublicKey // защищает IP и от следующих peer'ов в этом же ответе
		// Непустой кривой endpoint иначе попал бы в state, а UAPI wg-device его
		// отверг бы (рассинхрон state↔device). Зануляем → peer initiator-only,
		// валидный NodeIP сохраняем.
		endpoint := r.Endpoint
		if endpoint != "" && !ValidEndpoint(endpoint) {
			rejected = append(rejected, fmt.Sprintf("peer %s: invalid endpoint %q (dropped)",
				ShortKey(r.PublicKey), endpoint))
			endpoint = ""
		}
		newP := state.Peer{
			Label:     r.Label,
			PublicKey: r.PublicKey,
			Endpoint:  endpoint,
			NodeIP:    r.NodeIP,
			// НЕ доверяем is_seed из gossip (см. проход по local выше): новые узлы
			// приходят только не-seed'ами. Единственный seed известен с init/join.
			IsSeed:   false,
			LastSeen: now,
		}
		merged = append(merged, newP)
		changed = append(changed, newP)
		persist = true // новый peer — пишем на диск
	}

	return merged, changed, rejected, persist
}

// rejectNewPeer проверяет нового peer'а из gossip на безопасность mesh-IP.
// "" — принять; иначе причина отказа. Endpoint валидируется отдельно у caller'а
// (кривой непустой зануляется, чтобы не уехал рассинхрон state↔device).
func rejectNewPeer(r state.Peer, ipOwner map[string]string, cidr *net.IPNet, cidrStr string) string {
	ip := net.ParseIP(r.NodeIP)
	if r.NodeIP == "" || ip == nil {
		return fmt.Sprintf("peer %s: invalid node_ip %q", ShortKey(r.PublicKey), r.NodeIP)
	}
	if cidr != nil && !cidr.Contains(ip) {
		return fmt.Sprintf("peer %s: node_ip %s outside mesh cidr %s",
			ShortKey(r.PublicKey), r.NodeIP, cidrStr)
	}
	if owner, taken := ipOwner[r.NodeIP]; taken && owner != r.PublicKey {
		return fmt.Sprintf("peer %s: node_ip %s already owned by %s (ip-hijack rejected)",
			ShortKey(r.PublicKey), r.NodeIP, ShortKey(owner))
	}
	return ""
}

// ValidEndpoint — endpoint имеет вид host:port с НЕПУСТЫМ host и ЧИСЛОВЫМ port.
// net.SplitHostPort сам по себе пропускает «host:notaport» и «:port» — а такой
// «endpoint» дошёл бы до UAPI wg-device и был бы отвергнут уже там (а hostname ещё
// и дёрнул бы блокирующий DNS-resolve в gossip-горутине). Поэтому порт проверяем
// явно. Пустой endpoint валиден на уровне домена (peer initiator-only) — непустоту
// проверяй отдельно. Используется и merge (gossip), и bootstrap (join) — единая граница.
//
// NB: hostname:port здесь проходит (host непустой) — endpoint у нас может быть и
// hostname'ом. Если потребуется строго IP:port (убрать DNS из IpcSet) — netip.ParseAddrPort.
func ValidEndpoint(endpoint string) bool {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" {
		return false
	}
	if _, err := strconv.Atoi(port); err != nil {
		return false
	}
	return true
}

// ShortKey укорачивает pubkey для лога/причин (полный — 44 base64-символа).
// Экспортирован, чтобы транспорт (bootstrap/gossip) не дублировал тот же helper:
// mesh — нижний слой, его уже импортируют все control-plane-пакеты.
func ShortKey(k string) string {
	if len(k) > 12 {
		return k[:8] + "..."
	}
	return k
}
