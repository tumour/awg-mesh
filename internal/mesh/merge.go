// Package mesh — доменное ядро mesh-сети: аллокация IP, merge peer-list'а,
// построение статуса. Платформо-НЕЗАВИСИМО (ни CLI, ни HTTP, ни ОС-вызовов),
// зависит только от internal/state. Единый источник доменной логики для всех
// фронтендов (CLI, --json, web-дашборд, LuCI) и control-plane (gossip, bootstrap).
package mesh

import (
	"fmt"
	"net"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// MergePeers — мерж локального peer-list'а с peer-list'ом remote-ноды (gossip).
// Работает на доменной модели state.Peer (caller конвертирует свой wire-тип в
// state.Peer заранее — домен не знает про gossip/proto-форматы).
//
// Возвращает (merged, changed, rejected):
//
//	merged   — полный новый список для state.Peers (обновлённые endpoint'ы
//	           существующих peer'ов + refresh'нутые LastSeen).
//	changed  — что пушить в wg-device через UpdatePeer (новые peers + те, у кого
//	           сменился endpoint). Pure refresh LastSeen в changed не идёт.
//	rejected — человекочитаемые причины отказа (для лога caller'ом).
//
// БЕЗОПАСНОСТЬ (trust-by-tunneling плоское: любая нода в mesh может прислать
// произвольный peer-list). Чтобы одна нода не угнала чужой mesh-IP/маршрут,
// нового peer'а отвергаем, если его NodeIP: невалиден, вне networkCIDR, или уже
// принадлежит ДРУГОМУ pubkey (коллизия → cryptokey-routing last-write-wins отдал
// бы /32 атакующему). NodeIP существующих peer'ов через gossip не меняется
// (матчим по pubkey), поэтому угон возможен только через «нового» peer'а.
//
// Себя из remote всегда отфильтровываем (selfPub); себя из local сохраняем как
// есть. Удаление peer'ов (revoke/tombstone) — v0.2, сейчас union-with-update.
//
// Честность LastSeen: refresh означает «remote-нода знает этого peer'а», а НЕ
// «peer жив» — прямого health-check'а нет. Не строить на этом expiry-логику.
//
// ОСТАЁТСЯ открытым (нужны подписи, v0.2): forged-but-valid endpoint существующего
// peer'а. MergePeers применяет смену endpoint'а от любого источника (last-pull-wins),
// поэтому злая нода может перенаправить трафик к соседу в никуда (DoS). Здесь
// валидируется только ФОРМАТ endpoint'а, не его подлинность.
func MergePeers(local, remote []state.Peer, selfPub, networkCIDR string) (merged, changed []state.Peer, rejected []string) {
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
		if endpointChanged {
			if _, _, err := net.SplitHostPort(r.Endpoint); err != nil {
				rejected = append(rejected, fmt.Sprintf("peer %s: invalid endpoint %q",
					shortKey(p.PublicKey), r.Endpoint))
				endpointChanged = false
			} else {
				updated.Endpoint = r.Endpoint
			}
		}
		if r.Label != "" && r.Label != p.Label {
			updated.Label = r.Label
		}
		if r.IsSeed != p.IsSeed {
			updated.IsSeed = r.IsSeed
		}
		merged = append(merged, updated)
		// В wg-device пушим только при endpoint-смене — label/IsSeed на wg не влияют.
		if endpointChanged {
			changed = append(changed, updated)
		}
	}

	// Новые peers — те что есть в remote, но не в local. seenNew дедуплицирует
	// дубликаты pubkey в самом remote-ответе, сохраняя порядок remote.
	seenNew := make(map[string]bool)
	for _, r := range remote {
		if r.PublicKey == selfPub || localKeys[r.PublicKey] || seenNew[r.PublicKey] {
			continue
		}
		if reason := rejectNewPeer(r, ipOwner, cidr, networkCIDR); reason != "" {
			rejected = append(rejected, reason)
			continue
		}
		seenNew[r.PublicKey] = true
		ipOwner[r.NodeIP] = r.PublicKey // защищает IP и от следующих peer'ов в этом же ответе
		newP := state.Peer{
			Label:     r.Label,
			PublicKey: r.PublicKey,
			Endpoint:  r.Endpoint,
			NodeIP:    r.NodeIP,
			IsSeed:    r.IsSeed,
			LastSeen:  now,
		}
		merged = append(merged, newP)
		changed = append(changed, newP)
	}

	return merged, changed, rejected
}

// rejectNewPeer проверяет нового peer'а из gossip на безопасность mesh-IP.
// "" — принять; иначе причина отказа. Endpoint-формат валидируется отдельно
// (для нового peer'а пустой/кривой endpoint не угроза — он просто initiator-only).
func rejectNewPeer(r state.Peer, ipOwner map[string]string, cidr *net.IPNet, cidrStr string) string {
	ip := net.ParseIP(r.NodeIP)
	if r.NodeIP == "" || ip == nil {
		return fmt.Sprintf("peer %s: invalid node_ip %q", shortKey(r.PublicKey), r.NodeIP)
	}
	if cidr != nil && !cidr.Contains(ip) {
		return fmt.Sprintf("peer %s: node_ip %s outside mesh cidr %s",
			shortKey(r.PublicKey), r.NodeIP, cidrStr)
	}
	if owner, taken := ipOwner[r.NodeIP]; taken && owner != r.PublicKey {
		return fmt.Sprintf("peer %s: node_ip %s already owned by %s (ip-hijack rejected)",
			shortKey(r.PublicKey), r.NodeIP, shortKey(owner))
	}
	return ""
}

// shortKey укорачивает pubkey для лога/причин (полный — 44 base64-символа).
func shortKey(k string) string {
	if len(k) > 12 {
		return k[:8] + "..."
	}
	return k
}
