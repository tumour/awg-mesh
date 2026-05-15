package main

import (
	"flag"
	"fmt"

	"github.com/tumour/awg-mesh/internal/jointoken"
)

// cmdJoin — bootstrap новой ноды через token от seed'а.
//
// Текущая итерация: только парсит токен и валидирует поля. Полный
// Noise_IKpsk2 handshake + state-exchange — следующий коммит.
func cmdJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	label := fs.String("label", "", "human-readable node label (required), e.g. 'hetzner'")
	tokenStr := fs.String("token", "", "join-token from `meshd init` output (required)")
	fs.Parse(args)

	if *label == "" {
		return fmt.Errorf("--label is required")
	}
	if *tokenStr == "" {
		return fmt.Errorf("--token is required")
	}

	tok, err := jointoken.Decode(*tokenStr)
	if err != nil {
		return fmt.Errorf("decode token: %w", err)
	}

	fmt.Printf("token parsed OK:\n  seed endpoint: %s\n  seed pubkey:   %s\n  secret:        %s...%s (52 chars)\n",
		tok.SeedEndpoint, tok.SeedPubKey,
		tok.Secret[:8], tok.Secret[len(tok.Secret)-4:])
	return fmt.Errorf("handshake not implemented yet — coming in next iteration")
}
