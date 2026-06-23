package main

import (
	"context"
	"flag"
	"log/slog"
	"os/signal"
	"syscall"
	"time"

	"github.com/tumour/awg-mesh/internal/gossip"
	"github.com/tumour/awg-mesh/internal/node"
	"github.com/tumour/awg-mesh/internal/state"
)

// cmdRun — запускает демон meshd. Тонкая обёртка над node.Run: парс флагов,
// обработка сигналов и host-integration (UFW-warn). Вся оркестрация — в node.
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	iface := fs.String("interface", "awg0", "TUN interface name")
	verbose := fs.Bool("verbose", false, "enable verbose AmneziaWG-device logs")
	gossipInterval := fs.Duration("gossip-interval", 60*time.Second,
		"how often to pull peer-list from a random peer (0 = disabled)")
	fs.Parse(args)

	logger := newDaemonLogger()
	// SetDefault — здесь ОК: это CLI-точка входа, демон владеет процессом, и либы,
	// дёргающие slog.Default(), получат наш sink. node.Run глобал НЕ трогает
	// (логгер инъектится через Options) — поэтому встраивание meshd в чужой процесс
	// не перетопчет его slog.Default().
	slog.SetDefault(logger)

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	return node.Run(ctx, node.Options{
		StateFile:      *stateFlag,
		Interface:      *iface,
		Verbose:        *verbose,
		GossipInterval: *gossipInterval,
		Logger:         logger,
		FirewallWarn:   func(iface string) { warnFirewallUFW(logger, iface) },
	})
}

// warnFirewallUFW — host-integration: если UFW активен и блокирует gossip на
// mesh-интерфейсе, предупреждаем в лог (firewall meshd сам не трогает; см. ufw.go).
func warnFirewallUFW(logger *slog.Logger, iface string) {
	active, rules := ufwStatus()
	if active && !ufwMeshRuleExists(rules) {
		logger.Warn("ufw is active and blocks incoming gossip on mesh interface — "+
			"peers can't pull peer-list; fix: ufw allow in on this iface to gossip port",
			"iface", iface, "gossip_port", gossip.DefaultPort)
	}
}
