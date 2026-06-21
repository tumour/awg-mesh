package main

import (
	"flag"
	"fmt"
	"net"
	"strconv"

	"github.com/tumour/awg-mesh/internal/node"
	"github.com/tumour/awg-mesh/internal/state"
)

// cmdJoin — подключение ноды к существующему mesh'у через token. Тонкая обёртка:
// флаги → node.Join → печать + host-integration (firewall/autostart).
func cmdJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	label := fs.String("label", "", "human-readable node label (required), e.g. 'hetzner'")
	tokenStr := fs.String("token", "", "join-token from `meshd init` output (required)")
	publicEndpoint := fs.String("public-endpoint", "",
		"public endpoint announced to peers (host:port), e.g. <public-ip>:51820 — "+
			"set on nodes with a public IP so others can dial them directly; "+
			"omit on NAT-ed nodes")
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	noAutoStart := fs.Bool("no-auto-start", false,
		"skip starting meshd daemon via systemctl after join")
	ufwMode := fs.String("ufw", "",
		"opt-in UFW setup: 'gossip' (allow only peer-list sync, "+
			"9100/tcp on awg0) or 'all' (allow all mesh traffic to this node); "+
			"default: don't touch the firewall, just print a hint if needed")
	fs.Parse(args)

	if err := validateUFWMode(*ufwMode); err != nil {
		return err
	}

	// Порт из --public-endpoint станет нашим WG listen-port'ом — иначе
	// объявленный endpoint указывал бы на закрытую дверь.
	ownListenPort := 0
	if *publicEndpoint != "" {
		_, portStr, err := net.SplitHostPort(*publicEndpoint)
		if err != nil {
			return fmt.Errorf("invalid --public-endpoint %q: %w", *publicEndpoint, err)
		}
		p, err := strconv.Atoi(portStr)
		if err != nil || p < 1 || p > 65535 {
			return fmt.Errorf("invalid port in --public-endpoint: %q", portStr)
		}
		ownListenPort = p
	}

	res, err := node.Join(node.JoinParams{
		Label:          *label,
		Token:          *tokenStr,
		PublicEndpoint: *publicEndpoint,
		OwnListenPort:  ownListenPort,
		StateFile:      *stateFlag,
	})
	if err != nil {
		return err
	}

	endpointInfo := res.Endpoint
	if endpointInfo == "" {
		endpointInfo = "(none — NAT/initiator-only)"
	}
	fmt.Printf(`✓ joined mesh

  label:        %s
  network:      %s
  your ip:      %s
  endpoint:     %s
  pubkey:       %s
  seed:         %s
  peers known:  %d
  state file:   %s
`, res.Label, res.NetworkCIDR, res.NodeIP, endpointInfo, res.PublicKey,
		res.SeedEndpoint, res.PeersKnown, res.StateFile)

	// Firewall до старта демона — иначе run сразу заворчит в лог про gossip.
	applyUFWMode(*ufwMode)

	if !*noAutoStart {
		if !autoStartDaemon(*stateFlag) {
			printManualStartHint()
		}
	}

	return nil
}
