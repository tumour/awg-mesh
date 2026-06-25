package main

import (
	"flag"
	"fmt"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
)

// cmdSetParams — анонсирует flag-day-смену СЕТЕВЫХ awg-params (S3/S4/H) на всю
// сеть. Запускается ТОЛЬКО на seed (он authoritative по сетевым params). Кладёт
// новый набор в Pending БЕЗ момента применения и раздаёт по gossip. Момент flip
// (ApplyAt) seed назначит сам, когда ВСЕ ноды подтвердят приём (ack-then-commit) —
// так flip не стартует, пока кто-то не получил, и ни одна нода не теряется.
//
// Доменная логика — в mesh.NewPending; здесь только I/O.
func cmdSetParams(args []string) error {
	fs := flag.NewFlagSet("set-params", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	fs.Parse(args)

	store := state.NewStore(*stateFlag)
	s, err := store.Read()
	if err != nil {
		return err
	}
	if !s.IsSeed {
		return fmt.Errorf("set-params запускается только на seed (сетевые params раздаёт seed) — эта нода regular")
	}

	// Свежий 2.0-набор: непересекающиеся H-диапазоны + S3/S4 из реком. диапазонов.
	params, err := awgparams.Generate()
	if err != nil {
		return fmt.Errorf("generate params: %w", err)
	}

	var pending *state.PendingParams
	if _, err := store.Update(func(st *state.State) error {
		// Под локом, поверх свежей версии (gossip мог принять чужой Pending) —
		// так остаёмся монотонными даже при гонке.
		pending = mesh.NewPending(st.ParamsVersion, params)
		st.Pending = pending
		return nil
	}); err != nil {
		return err
	}

	fmt.Printf(`✓ flag-day анонсирован
  версия params:  %d → %d
  применить в:     будет назначено, когда ВСЕ ноды подтвердят приём (ack)
  раздача:         по gossip; flip синхронный, связь кратко прервётся в окне применения
`, s.ParamsVersion, pending.Version)
	return nil
}
