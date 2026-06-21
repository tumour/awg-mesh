package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
)

// cmdStatus — печатает состояние ноды: текстом (для глаз) или --json (для
// скриптов/мониторинга/веб). Данные собирает mesh.BuildStatus — единый источник
// для CLI, --json, web-дашборда и LuCI (без дублирования логики).
func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	asJSON := fs.Bool("json", false, "output machine-readable JSON")
	fs.Parse(args)

	s, err := state.Load(*stateFlag)
	if err != nil {
		return err
	}
	view := mesh.BuildStatus(s)

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(view)
	}

	fmt.Printf(`node:    %s (%s)
network: %s
node ip: %s
pubkey:  %s

peers (%d):
`, view.Label, view.Role, view.NetworkCIDR, view.NodeIP, view.PublicKey, len(view.Peers))

	for _, p := range view.Peers {
		marker := " "
		if p.IsSelf {
			marker = "*"
		}
		fmt.Printf("  %s %-15s  %-21s  %s\n", marker, p.Label, p.NodeIP, p.Endpoint)
	}

	return nil
}
