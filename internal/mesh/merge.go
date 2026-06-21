// Package mesh — доменное ядро mesh-сети: аллокация IP, merge peer-list'а,
// построение статуса. Платформо-НЕЗАВИСИМО (ни CLI, ни HTTP, ни ОС-вызовов),
// зависит только от internal/state. Единый источник доменной логики для всех
// фронтендов (CLI, --json, web-дашборд, LuCI) и control-plane (gossip, bootstrap).
package mesh

import (
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// MergePeers — мерж локального peer-list'а с peer-list'ом remote-ноды (gossip).
// Работает на доменной модели state.Peer (caller конвертирует свой wire-тип в
// state.Peer заранее — домен не знает про gossip/proto-форматы).
//
// Возвращает (merged, changed):
//
//	merged  — полный новый список для state.Peers (обновлённые endpoint'ы
//	          существующих peer'ов + refresh'нутые LastSeen).
//	changed — что пушить в wg-device через UpdatePeer (новые peers + те, у кого
//	          сменился endpoint). Pure refresh LastSeen в changed не идёт —
//	          wg-device от него не зависит.
//
// Себя из remote всегда отфильтровываем (selfPub); себя из local сохраняем как
// есть. Удаление peer'ов (revoke/tombstone) — v0.2, сейчас union-with-update.
//
// Честность LastSeen: refresh означает «remote-нода знает этого peer'а», а НЕ
// «peer жив» — прямого health-check'а нет. Не строить на этом expiry-логику.
//
// Конфликты endpoint'ов разрешаются last-pull-wins: записи не версионированы,
// поэтому при двух разных значениях итог зависит от порядка опросов (версионирование — v0.2).
func MergePeers(local, remote []state.Peer, selfPub string) (merged, changed []state.Peer) {
	// Индекс remote по pubkey, заодно отфильтровываем себя.
	rByKey := make(map[string]state.Peer, len(remote))
	for _, r := range remote {
		if r.PublicKey == selfPub {
			continue
		}
		rByKey[r.PublicKey] = r
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
		endpointChanged := r.Endpoint != "" && r.Endpoint != p.Endpoint
		if endpointChanged {
			updated.Endpoint = r.Endpoint
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
	// дубликаты pubkey в самом remote-ответе (иначе добавили бы peer'а дважды),
	// сохраняя порядок remote.
	seenNew := make(map[string]bool)
	for _, r := range remote {
		if r.PublicKey == selfPub || localKeys[r.PublicKey] || seenNew[r.PublicKey] {
			continue
		}
		seenNew[r.PublicKey] = true
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

	return merged, changed
}
