package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"time"

	"github.com/tumour/awg-mesh/internal/gossip"
	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
)

// cmdLeave — чистый выход ноды из mesh. Нода объявляет свой ПЕРМАНЕНТНЫЙ tombstone
// (по своему pubkey) и пушит его endpoint-пирам: они снимут её с wg-device и
// перекроют реанонс, остальные узнают по gossip. Под NAT это единственный способ
// сообщить о себе — pull в её сторону не инициируется (mesh.GossipCandidates).
//
// Best-effort: если ни один пир не принял push (нода уже изолирована), сеть забудет
// её только через seed-side revoke — об этом печатаем предупреждение. Сам демон
// leave НЕ останавливает (init-систему надёжно не знаем) — печатает команду.
func cmdLeave(args []string) error {
	fs := flag.NewFlagSet("leave", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	yes := fs.Bool("yes", false, "не спрашивать подтверждение (для скриптов)")
	fs.Parse(args)

	store := state.NewStore(*stateFlag)
	s, err := store.Read()
	if err != nil {
		return err
	}
	if s.IsSeed {
		return fmt.Errorf("leave на seed не поддержан: seed держит bootstrap и IP-аллокацию")
	}

	fmt.Printf("Покинуть mesh:\n  label:   %s\n  mesh-IP: %s\n  pubkey:  %s\n",
		label(s.NodeLabel), s.NodeIP, s.PublicKey)
	if !*yes {
		fmt.Print("Это перманентно (вернуться только с новым ключом). Продолжить? [y/N]: ")
		var ans string
		fmt.Scanln(&ans)
		if ans != "y" && ans != "Y" {
			fmt.Println("отменено")
			return nil
		}
	}

	self := state.Peer{Label: s.NodeLabel, PublicKey: s.PublicKey, NodeIP: s.NodeIP}
	ts := mesh.NewTombstone(self, time.Now())

	// Запоминаем свой отзыв локально — на случай рестарта демона до остановки, чтобы
	// не реанонсить себя обратно соседям.
	if _, err := store.Update(func(st *state.State) error {
		merged, added := mesh.MergeTombstones(st.Tombstones, []state.Tombstone{ts})
		if len(added) == 0 {
			return state.ErrNoChange
		}
		st.Tombstones = merged
		return nil
	}); err != nil {
		return err
	}

	// Push своего tombstone endpoint-пирам (через локальный wg-туннель, пока демон жив).
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	hc := &http.Client{Timeout: 10 * time.Second}
	accepted := 0
	for _, p := range mesh.GossipCandidates(s) { // только пиры с endpoint достижимы для POST
		if err := gossip.PushTombstone(ctx, hc, p.NodeIP, gossip.DefaultPort, ts); err != nil {
			fmt.Printf("  ✗ %s (%s): %v\n", p.NodeIP, label(p.Label), err)
			continue
		}
		accepted++
		fmt.Printf("  ✓ %s (%s) принял отзыв\n", p.NodeIP, label(p.Label))
	}

	if accepted == 0 {
		fmt.Println("⚠ ни один пир не принял отзыв — сеть забудет ноду только после `meshd revoke` на seed")
	} else {
		fmt.Printf("✓ отзыв принят: %d пир(ов) — разойдётся по сети gossip'ом\n", accepted)
	}
	fmt.Println("Теперь остановите демон:  systemctl stop meshd   (OpenWrt: /etc/init.d/meshd stop)")
	return nil
}
