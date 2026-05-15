package main

import (
	"flag"
	"fmt"

	"github.com/tumour/awg-mesh/internal/state"
)

// cmdStatus — печатает текущий state.json в человекочитаемом виде.
func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	fs.Parse(args)

	s, err := state.Load(*stateFlag)
	if err != nil {
		return err
	}

	role := "regular"
	if s.IsSeed {
		role = "seed"
	}

	fmt.Printf(`node:    %s (%s)
network: %s
node ip: %s
pubkey:  %s

peers (%d):
`, s.NodeLabel, role, s.NetworkCIDR, s.NodeIP, s.PublicKey, len(s.Peers))

	for _, p := range s.Peers {
		marker := " "
		if p.PublicKey == s.PublicKey {
			marker = "*"
		}
		fmt.Printf("  %s %-15s  %-21s  %s\n", marker, p.Label, p.NodeIP, p.Endpoint)
	}

	return nil
}
