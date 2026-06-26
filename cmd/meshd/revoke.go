package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
)

// cmdRevoke отзывает ноду из mesh: кладёт перманентный tombstone по её pubkey.
// Запускается ТОЛЬКО на seed. Tombstone раздаётся по gossip; каждая нода снимает
// отозванного с wg-device на лету (RemovePeer, без рестарта) и перекрывает его
// реанонс, а re-join тем же ключом отклоняется. Вернуть ноду можно лишь с НОВЫМ
// keypair (новый pubkey не под tombstone).
//
// Здесь только запись интента в state (как set-params/Pending): снятие с device и
// удаление из peer-list делает демон в gossip-цикле — единственный, кто трогает
// device, чтобы state и wg-device не разъехались. Доменная логика — mesh.NewTombstone.
func cmdRevoke(args []string) error {
	fs := flag.NewFlagSet("revoke", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	yes := fs.Bool("yes", false, "не спрашивать подтверждение (для скриптов)")
	fs.Parse(args)

	if fs.NArg() != 1 {
		return fmt.Errorf("usage: meshd revoke <mesh-ip|pubkey> — кого отозвать")
	}
	selector := fs.Arg(0)

	store := state.NewStore(*stateFlag)
	s, err := store.Read()
	if err != nil {
		return err
	}
	if !s.IsSeed {
		return fmt.Errorf("revoke запускается только на seed — эта нода regular")
	}

	target := findPeerBySelector(s.Peers, selector)
	if target == nil {
		return fmt.Errorf("нода %q не найдена в peer-list (ожидался mesh-IP или полный pubkey)", selector)
	}
	if target.PublicKey == s.PublicKey {
		return fmt.Errorf("нельзя отозвать саму себя (seed); используйте leave на ноде")
	}
	if mesh.IsRevoked(s.Tombstones, target.PublicKey) {
		fmt.Printf("нода %s (%s) уже отозвана — нечего делать\n", target.NodeIP, label(target.Label))
		return nil
	}

	// Печатаем ТОЧНУЮ идентичность отзываемого: селектор по label был бы опасен при
	// дублях (две записи с одной меткой, разными pubkey), поэтому подтверждаем pubkey явно.
	fmt.Printf("Отозвать ноду:\n  label:   %s\n  mesh-IP: %s\n  pubkey:  %s\n",
		label(target.Label), target.NodeIP, target.PublicKey)
	if !*yes {
		fmt.Print("Это перманентно (вернуть только с новым ключом). Продолжить? [y/N]: ")
		var ans string
		fmt.Scanln(&ans)
		if ans != "y" && ans != "Y" {
			fmt.Println("отменено")
			return nil
		}
	}

	if _, err := store.Update(func(st *state.State) error {
		// Под локом, поверх свежего state. Идемпотентно: если уже отозван — no-op.
		if mesh.IsRevoked(st.Tombstones, target.PublicKey) {
			return state.ErrNoChange
		}
		st.Tombstones = append(st.Tombstones, mesh.NewTombstone(*target, time.Now()))
		return nil
	}); err != nil {
		return err
	}

	fmt.Printf(`✓ revoke анонсирован
  нода:       %s (%s)
  применение: на этой и других нодах — в ближайшем gossip-цикле (RemovePeer на лету)
  раздача:    по gossip; реанонс перекрыт, re-join тем же ключом отклонится
`, target.NodeIP, label(target.Label))
	return nil
}

// findPeerBySelector ищет peer по mesh-IP или полному base64-pubkey (точное
// совпадение — префикс был бы неоднозначен и опасен для перманентного отзыва).
func findPeerBySelector(peers []state.Peer, selector string) *state.Peer {
	for i := range peers {
		if peers[i].NodeIP == selector || peers[i].PublicKey == selector {
			return &peers[i]
		}
	}
	return nil
}

// label подставляет заглушку для безымянной ноды (label не обязателен).
func label(l string) string {
	if l == "" {
		return "без метки"
	}
	return l
}
