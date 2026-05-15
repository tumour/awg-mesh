package main

import (
	"flag"
	"fmt"
)

// cmdJoin — заглушка для следующей итерации. Реальная имплементация добавится
// когда Noise_IKpsk1-handshake и bootstrap-протокол будут готовы.
func cmdJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	_ = fs.String("label", "", "human-readable node label (required)")
	_ = fs.String("seed", "", "seed endpoint (host:port, required)")
	_ = fs.String("secret", "", "cluster secret from `meshd init` output (required)")
	fs.Parse(args)

	return fmt.Errorf("not implemented yet — coming in next iteration")
}
