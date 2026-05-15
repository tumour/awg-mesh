package main

import (
	"encoding/base64"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

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
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	noAutoStart := fs.Bool("no-auto-start", false,
		"skip starting meshd daemon via systemctl after join")
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

	hs, err := handshake.InitiatorHandshake(priv[:], pub[:], seedPub, psk)
	if err != nil {
		return fmt.Errorf("init handshake: %w", err)
	}

	fmt.Printf("connecting to seed %s...\n", tok.SeedEndpoint)
	conn, err := net.DialTimeout("tcp", tok.SeedEndpoint, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial seed: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))

	// Message 1: client → server
	msg1, _, _, err := hs.WriteMessage(nil, nil)
	if err != nil {
		return fmt.Errorf("noise msg1: %w", err)
	}
	if err := writeFramed(conn, msg1); err != nil {
		return fmt.Errorf("send msg1: %w", err)
	}

	// Message 2: server → client (+ CipherStates)
	msg2, err := readFramed(conn, 2048)
	if err != nil {
		return fmt.Errorf("read msg2: %w", err)
	}
	_, csInitToResp, csRespToInit, err := hs.ReadMessage(nil, msg2)
	if err != nil {
		return fmt.Errorf("noise msg2 (wrong secret or seed pubkey?): %w", err)
	}

	// HelloRequest
	if err := proto.WriteMessage(conn, csInitToResp, proto.HelloRequest{
		Version:   proto.ProtoVersion,
		Label:     *label,
		PublicKey: pub.String(),
	}); err != nil {
		return fmt.Errorf("send hello-req: %w", err)
	}

	var resp proto.HelloResponse
	if err := proto.ReadMessage(conn, csRespToInit, &resp); err != nil {
		return fmt.Errorf("read hello-resp: %w", err)
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

	newState := &state.State{
		Version:       1,
		NodeLabel:     *label,
		ClusterSecret: tok.Secret,
		AwgParams:     resp.AwgParams,
		NetworkCIDR:   resp.NetworkCIDR,
		PrivateKey:    priv.String(),
		PublicKey:     pub.String(),
		NodeIP:        resp.YourIP,
		ListenPort:    resp.WGPort, // для будущей WG-инициации
		IsSeed:        false,
		Peers:         peers,
	}
	if existing != nil {
		newState.CreatedAt = existing.CreatedAt
	}
	if err := newState.Save(*stateFlag); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Printf(`✓ joined mesh

  label:        %s
  network:      %s
  your ip:      %s
  pubkey:       %s
  seed:         %s
  peers known:  %d
  state file:   %s
`, *label, resp.NetworkCIDR, resp.YourIP, pub.String(),
		tok.SeedEndpoint, len(peers), *stateFlag)

	if !*noAutoStart {
		if !autoStartDaemon(*stateFlag) {
			printManualStartHint()
		}
	}

	return nil
}
