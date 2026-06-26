package mesh

import (
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

// Доменное ядро revoke/leave — отзыва ноды из mesh через перманентный tombstone.
//
// Проблема, которую это решает: MergePeers — union (merge.go), он НИКОГДА не удаляет
// peer'ов сам и воскрешает любого валидного, кого прислал сосед. Поэтому «забыть»
// ноду нельзя простым вычёркиванием из Peers — на следующем gossip-pull она вернётся.
// Tombstone — это «анти-peer»: отозванный pubkey перекрывает реанонс в MergePeers и
// снимается с wg-device на лету (RemovePeer), без пересоздания интерфейса и рестарта.
//
// Перманентность по pubkey: отозванный ключ мёртв навсегда; ноду возвращают re-join'ом
// с НОВЫМ keypair (новый pubkey не под tombstone). Это делает приём идемпотентным
// (повтор — no-op) и убирает необходимость анти-replay / un-revoke.
//
// Здесь только чистые решения (без сети/ОС/времени-как-глобала); применение к state и
// device — в internal/node, транспорт — в internal/gossip. Покрыто таблицами.

// NewTombstone формирует отзыв peer'а: его pubkey + метка (для аудита/лога) + момент
// отзыва. Время передаётся извне — домен не ходит к часам.
func NewTombstone(p state.Peer, now time.Time) state.Tombstone {
	return state.Tombstone{
		PublicKey: p.PublicKey,
		Label:     p.Label,
		RevokedAt: now.UTC(),
	}
}

// IsRevoked сообщает, отозван ли pubkey (присутствует ли он среди tombstones).
// Используется MergePeers для перекрытия реанонса отозванной ноды.
func IsRevoked(tombstones []state.Tombstone, pubkey string) bool {
	for _, t := range tombstones {
		if t.PublicKey == pubkey {
			return true
		}
	}
	return false
}

// MergeTombstones объединяет локальный набор отзывов с присланным через gossip.
// Union по pubkey: уже известный tombstone не трогаем (перманентен → идемпотентно),
// новый добавляем. Возвращает (merged, added): merged — полный новый набор для
// state.Tombstones; added — только впервые увиденные отзывы. По added caller решает,
// что применить (RemovePeer + убрать из Peers) и нужно ли писать state на диск.
// Порядок детерминирован: сперва local как есть, затем новые из remote в порядке remote.
func MergeTombstones(local, remote []state.Tombstone) (merged, added []state.Tombstone) {
	seen := make(map[string]bool, len(local)+len(remote))
	merged = make([]state.Tombstone, 0, len(local)+len(remote))
	for _, t := range local {
		if t.PublicKey == "" || seen[t.PublicKey] {
			continue // пустой pubkey — мусор; дедуп на случай дублей в local
		}
		seen[t.PublicKey] = true
		merged = append(merged, t)
	}
	for _, t := range remote {
		// Пустой pubkey отбрасываем: иначе IsRevoked("") вернул бы true и сосед мог бы
		// флудить state бессодержательными tombstone'ами (flash-wear на роутере).
		if t.PublicKey == "" || seen[t.PublicKey] {
			continue
		}
		seen[t.PublicKey] = true
		merged = append(merged, t)
		added = append(added, t)
	}
	return merged, added
}

// ApplyTombstones делит peers на оставшихся (kept) и отозванных (removed). Чистая
// проекция: caller для каждого removed зовёт device.RemovePeer, а kept пишет в
// state.Peers.
//
// selfPub НИКОГДА не попадает в removed — даже под собственным tombstone. seed
// держит себя в Peers (init), а отзыв принимается trust-by-tunneling, поэтому без
// этой защиты форж tombstone(selfPub) через gossip заставил бы ноду удалить себя из
// своего же peer-list (и перестать анонсировать seed-endpoint новичкам). Тот же
// инвариант, что self-ветка в MergePeers. Свой уход — отдельный путь leave в node,
// по явной команде оператора, а не по входящему gossip.
func ApplyTombstones(peers []state.Peer, tombstones []state.Tombstone, selfPub string) (kept, removed []state.Peer) {
	if len(tombstones) == 0 {
		return peers, nil
	}
	revoked := make(map[string]bool, len(tombstones))
	for _, t := range tombstones {
		revoked[t.PublicKey] = true
	}
	for _, p := range peers {
		if p.PublicKey != selfPub && revoked[p.PublicKey] {
			removed = append(removed, p)
		} else {
			kept = append(kept, p)
		}
	}
	return kept, removed
}
