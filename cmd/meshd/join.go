package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/tumour/awg-mesh/internal/bootstrap"
	"github.com/tumour/awg-mesh/internal/clusterkey"
	"github.com/tumour/awg-mesh/internal/handshake"
	"github.com/tumour/awg-mesh/internal/jointoken"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// cmdJoin — bootstrap новой ноды через token.
//
// Парсит token, генерит локальный keypair, открывает TCP к seed-endpoint'у,
// выполняет Noise_IKpsk2 handshake. После handshake шлёт HelloRequest,
// получает HelloResponse с peer-list'ом, сохраняет state.json.
//
// Идемпотентность: повторный join с тем же keypair'ом → seed вернёт существующий
// IP, state.json пересохранится с актуальным peer-list'ом.
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

	if *label == "" {
		return fmt.Errorf("--label is required")
	}
	if *tokenStr == "" {
		return fmt.Errorf("--token is required")
	}
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

	tok, err := jointoken.Decode(*tokenStr)
	if err != nil {
		return fmt.Errorf("decode token: %w", err)
	}

	// Если state-file уже существует — используем тот же keypair (resume).
	// Иначе генерируем новый.
	var priv wgkey.Private
	var existing *state.State
	if s, err := state.Load(*stateFlag); err == nil {
		existing = s
		priv, err = wgkey.ParsePrivate(s.PrivateKey)
		if err != nil {
			return fmt.Errorf("parse existing private key: %w", err)
		}
		fmt.Printf("resuming with existing keypair from %s\n", *stateFlag)
	} else if !os.IsNotExist(err) && err != state.ErrNotInitialized {
		return fmt.Errorf("load state: %w", err)
	} else {
		priv, err = wgkey.GeneratePrivate()
		if err != nil {
			return fmt.Errorf("generate private key: %w", err)
		}
	}
	pub := priv.Public()

	// Готовим PSK из cluster-secret.
	cs, err := clusterkey.Parse(tok.Secret)
	if err != nil {
		return fmt.Errorf("parse cluster secret: %w", err)
	}
	psk, err := handshake.DerivePSK(cs[:])
	if err != nil {
		return fmt.Errorf("derive psk: %w", err)
	}

	seedPub, err := base64.StdEncoding.DecodeString(tok.SeedPubKey)
	if err != nil {
		return fmt.Errorf("parse seed pubkey: %w", err)
	}
	if len(seedPub) != 32 {
		return fmt.Errorf("seed pubkey wrong size: %d", len(seedPub))
	}

	fmt.Printf("connecting to seed %s...\n", tok.SeedEndpoint)
	resp, err := bootstrap.Join(tok.SeedEndpoint, psk, priv, pub, seedPub, proto.HelloRequest{
		Version:   proto.ProtoVersion,
		Label:     *label,
		PublicKey: pub.String(),
		Endpoint:  *publicEndpoint,
	})
	if err != nil {
		return err
	}
	if resp.Status != "ok" {
		return fmt.Errorf("seed rejected: %s", resp.Error)
	}

	// Конвертация peer-list'а в локальный формат state.
	peers := make([]state.Peer, 0, len(resp.Peers))
	for _, p := range resp.Peers {
		peers = append(peers, state.Peer{
			Label:     p.Label,
			PublicKey: p.PublicKey,
			Endpoint:  p.Endpoint,
			NodeIP:    p.NodeIP,
			IsSeed:    p.IsSeed,
		})
	}

	// При resume без --public-endpoint сохраняем прежний listen-port: нода
	// могла объявить endpoint в прошлый join, и seed его помнит (наша
	// peer-запись в ответе) — порт должен продолжать совпадать.
	if ownListenPort == 0 && existing != nil {
		ownListenPort = existing.ListenPort
	}

	newState := &state.State{
		Version:       1,
		NodeLabel:     *label,
		ClusterSecret: tok.Secret,
		AwgParams:     resp.AwgParams,
		NetworkCIDR:   resp.NetworkCIDR,
		PrivateKey:    priv.String(),
		PublicKey:     pub.String(),
		NodeIP:        resp.YourIP,
		ListenPort:    ownListenPort,
		IsSeed:        false,
		Peers:         peers,
	}
	if existing != nil {
		newState.CreatedAt = existing.CreatedAt
	}
	if err := newState.Save(*stateFlag); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// Показываем фактический endpoint из ответа seed'а: при resume без
	// --public-endpoint seed помнит объявленный ранее.
	endpointInfo := ""
	for _, p := range peers {
		if p.PublicKey == pub.String() {
			endpointInfo = p.Endpoint
			break
		}
	}
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
`, *label, resp.NetworkCIDR, resp.YourIP, endpointInfo, pub.String(),
		tok.SeedEndpoint, len(peers), *stateFlag)

	// Firewall до старта демона — иначе run сразу заворчит в лог про gossip.
	applyUFWMode(*ufwMode)

	if !*noAutoStart {
		if !autoStartDaemon(*stateFlag) {
			printManualStartHint()
		}
	}

	return nil
}
