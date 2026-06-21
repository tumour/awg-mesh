package main

import (
	"flag"
	"fmt"

	"github.com/tumour/awg-mesh/internal/node"
	"github.com/tumour/awg-mesh/internal/state"
)

// cmdInit — инициализация новой mesh-сети (первая нода = seed). Тонкая обёртка:
// флаги → node.Init → печать + host-integration (firewall/autostart).
func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	label := fs.String("label", "", "human-readable node label (required), e.g. 'beget'")
	listenAddr := fs.String("listen", ":51820", "WireGuard listen address (host:port)")
	publicEndpoint := fs.String("public-endpoint", "",
		"public endpoint announced to peers (host:port) — usually <public-ip>:51820")
	cidr := fs.String("cidr", "100.64.0.0/24",
		"mesh network CIDR (default CGNAT range, не пересекается с домашними LAN'ами)")
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	noAutoStart := fs.Bool("no-auto-start", false,
		"skip starting meshd daemon via systemctl after init")
	ufwMode := fs.String("ufw", "",
		"opt-in UFW setup: 'gossip' (allow only peer-list sync, "+
			"9100/tcp on awg0) or 'all' (allow all mesh traffic to this node); "+
			"default: don't touch the firewall, just print a hint if needed")
	fs.Parse(args)

	if err := validateUFWMode(*ufwMode); err != nil {
		return err
	}

	res, err := node.Init(node.InitParams{
		Label:          *label,
		ListenPort:     parsePort(*listenAddr),
		PublicEndpoint: *publicEndpoint,
		CIDR:           *cidr,
		StateFile:      *stateFlag,
	})
	if err != nil {
		return err
	}

	fmt.Printf(`✓ mesh initialized

  label:            %s
  network:          %s
  node ip:          %s
  public endpoint:  %s
  state file:       %s

`, res.Label, res.CIDR, res.NodeIP, res.PublicEndpoint, res.StateFile)

	// Firewall до старта демона — иначе run сразу заворчит в лог про gossip.
	applyUFWMode(*ufwMode)

	if !*noAutoStart {
		if !autoStartDaemon(*stateFlag) {
			printManualStartHint()
		}
	}

	fmt.Printf(`
To onboard another node, run on it:

  meshd join --label <node-name> --token %s

Token contains the cluster-secret — keep it confidential (send via scp/ssh,
not chat). Anyone with the token can join this mesh.
`, res.JoinToken)

	return nil
}
