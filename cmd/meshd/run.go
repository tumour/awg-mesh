package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	awgdev "github.com/amnezia-vpn/amneziawg-go/device"

	"github.com/tumour/awg-mesh/internal/clusterkey"
	"github.com/tumour/awg-mesh/internal/handshake"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wg"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// cmdRun — главный foreground-daemon: поднимает AmneziaWG-device,
// конфигурирует через state.peers, если seed — параллельно слушает
// bootstrap-сокет для новых join'ов.
//
// Идемпотентность: device пересоздаётся каждый запуск. Peers применяются
// заново. State на диске — source of truth.
//
// Завершение: SIGTERM/SIGINT → graceful shutdown (device.Close + ip link down).
func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	iface := fs.String("interface", "awg0", "TUN interface name")
	verbose := fs.Bool("verbose", false, "enable verbose AmneziaWG-device logs")
	fs.Parse(args)

	s, err := state.Load(*stateFlag)
	if err != nil {
		return err
	}

	priv, err := wgkey.ParsePrivate(s.PrivateKey)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	pub := priv.Public()

	logLevel := awgdev.LogLevelError
	if *verbose {
		logLevel = awgdev.LogLevelVerbose
	}

	// Seed слушает на listen_port; обычный peer без public-endpoint — ephemeral.
	listenPort := 0
	if s.IsSeed {
		listenPort = s.ListenPort
	}

	device, err := wg.New(*iface, listenPort, logLevel)
	if err != nil {
		return fmt.Errorf("create wg device: %w", err)
	}
	defer device.Close()

	log.Printf("meshd run: created interface %s (mesh-ip=%s, peers=%d, seed=%v)",
		device.Name(), s.NodeIP, len(s.Peers), s.IsSeed)

	if err := device.Configure(priv, s.AwgParams, s.Peers, pub); err != nil {
		return fmt.Errorf("configure device: %w", err)
	}
	if err := device.Up(); err != nil {
		return fmt.Errorf("bring device up: %w", err)
	}

	cidrSuffix := cidrPrefixSuffix(s.NetworkCIDR)
	if err := device.AssignIP(s.NodeIP + cidrSuffix); err != nil {
		return fmt.Errorf("assign ip: %w", err)
	}
	log.Printf("meshd run: %s up, mesh-ip=%s%s", device.Name(), s.NodeIP, cidrSuffix)

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	if s.IsSeed {
		go runBootstrapListener(ctx, *stateFlag, priv, pub, device)
	}

	log.Printf("meshd run: ready, waiting for signals")
	<-ctx.Done()
	log.Printf("meshd run: received signal, shutting down")

	if out, err := exec.Command("ip", "link", "set", "down", "dev", device.Name()).CombinedOutput(); err != nil {
		log.Printf("warn: ip link down: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// runBootstrapListener — версия cmdServe адаптированная для встроенного
// запуска внутри meshd run. Также обновляет live wg-device при добавлении
// нового peer'а через UpdatePeer (incremental UAPI).
func runBootstrapListener(
	ctx context.Context,
	statePath string,
	priv wgkey.Private,
	pub wgkey.Public,
	device *wg.Device,
) {
	s, err := state.Load(statePath)
	if err != nil {
		log.Printf("bootstrap: reload state: %v", err)
		return
	}
	cs, err := clusterkey.Parse(s.ClusterSecret)
	if err != nil {
		log.Printf("bootstrap: parse cluster secret: %v", err)
		return
	}
	psk, err := handshake.DerivePSK(cs[:])
	if err != nil {
		log.Printf("bootstrap: derive psk: %v", err)
		return
	}

	addr := fmt.Sprintf(":%d", s.ListenPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Printf("bootstrap: listen %s: %v", addr, err)
		return
	}
	go func() { <-ctx.Done(); _ = listener.Close() }()

	log.Printf("bootstrap: listening on %s/tcp", addr)

	var stateMu sync.Mutex
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("bootstrap: accept: %v", err)
			continue
		}
		go func(c net.Conn) {
			handleConn(c, statePath, priv, pub, psk, &stateMu)
			// После регистрации peer'а — пушим в live wg-device
			pushNewPeersToDevice(statePath, device, pub)
		}(conn)
	}
}

// pushNewPeersToDevice — incremental UAPI update: добавляет в running device
// каждого peer'а из state.json (idempotent — повторное добавление того же
// pubkey'а просто обновляет allowed-ip/endpoint, не создаёт дубликат).
func pushNewPeersToDevice(statePath string, dev *wg.Device, selfPub wgkey.Public) {
	s, err := state.Load(statePath)
	if err != nil {
		log.Printf("push-peers: reload state: %v", err)
		return
	}
	for _, p := range s.Peers {
		if p.PublicKey == selfPub.String() {
			continue
		}
		if err := dev.UpdatePeer(p); err != nil {
			log.Printf("push-peers: update %s: %v", p.Label, err)
		}
	}
}

// cidrPrefixSuffix вытаскивает "/24" из "100.64.0.0/24".
func cidrPrefixSuffix(cidr string) string {
	if idx := strings.IndexByte(cidr, '/'); idx >= 0 {
		return cidr[idx:]
	}
	return "/24"
}
