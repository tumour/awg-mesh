package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"

	"github.com/tumour/awg-mesh/internal/bootstrap"
	"github.com/tumour/awg-mesh/internal/clusterkey"
	"github.com/tumour/awg-mesh/internal/handshake"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// cmdServe — bootstrap-listener на seed-ноде для отладки (в демоне `meshd run`
// он встроен). Тонкая обёртка: грузит state/ключи и отдаёт всё в bootstrap.Serve.
func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	listenAddr := fs.String("listen", "",
		"bootstrap listen address (default :<state.listen_port>)")
	fs.Parse(args)

	store := state.NewStore(*stateFlag)
	s, err := store.Read()
	if err != nil {
		return err
	}
	if !s.IsSeed {
		return fmt.Errorf("this node is not a seed (is_seed=false) — cannot serve bootstrap")
	}

	addr := *listenAddr
	if addr == "" {
		addr = fmt.Sprintf(":%d", s.ListenPort)
	}

	priv, err := wgkey.ParsePrivate(s.PrivateKey)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	pub := priv.Public()
	cs, err := clusterkey.Parse(s.ClusterSecret)
	if err != nil {
		return fmt.Errorf("parse cluster secret: %w", err)
	}
	psk, err := handshake.DerivePSK(cs[:])
	if err != nil {
		return fmt.Errorf("derive psk: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	log.Printf("meshd serve: seed=%s peers=%d", s.NodeLabel, len(s.Peers))
	return bootstrap.Serve(ctx, addr, store, priv, pub, psk, nil)
}
