package node

import (
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/tumour/awg-mesh/internal/bootstrap"
	"github.com/tumour/awg-mesh/internal/clusterkey"
	"github.com/tumour/awg-mesh/internal/handshake"
	"github.com/tumour/awg-mesh/internal/jointoken"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// JoinParams — параметры подключения ноды к существующему mesh'у.
type JoinParams struct {
	Label          string
	Token          string
	PublicEndpoint string // host:port (пусто = за NAT)
	OwnListenPort  int    // порт из public-endpoint (0 = ephemeral/NAT)
	StateFile      string
}

// JoinResult — итог Join: что записано в state после ответа seed'а.
type JoinResult struct {
	Label        string
	NetworkCIDR  string
	NodeIP       string
	Endpoint     string // фактический endpoint нашей ноды (пусто = NAT)
	PublicKey    string
	SeedEndpoint string
	PeersKnown   int
	StateFile    string
}

// Join подключает ноду к mesh'у через join-token: keypair (resume или новый),
// Noise-bootstrap к seed'у, сохранение полученного state. Чистая операция — без
// CLI/печати/firewall/autostart. Импортируема и тестируема (вебморда дёргает
// напрямую).
//
// Идемпотентность: повторный join с тем же keypair'ом → seed вернёт прежний IP.
// Запись через Store при resume (RMW-safe, если демон уже крутится в процессе).
func Join(p JoinParams) (JoinResult, error) {
	if p.Label == "" {
		return JoinResult{}, fmt.Errorf("label is required")
	}
	if p.Token == "" {
		return JoinResult{}, fmt.Errorf("token is required")
	}
	tok, err := jointoken.Decode(p.Token)
	if err != nil {
		return JoinResult{}, fmt.Errorf("decode token: %w", err)
	}

	store := state.NewStore(p.StateFile)

	// Keypair resume: если state уже есть — тот же keypair; иначе генерим новый.
	var priv wgkey.Private
	var existing *state.State
	if s, err := store.Read(); err == nil {
		existing = s
		priv, err = wgkey.ParsePrivate(s.PrivateKey)
		if err != nil {
			return JoinResult{}, fmt.Errorf("parse existing private key: %w", err)
		}
	} else if !errors.Is(err, state.ErrNotInitialized) {
		return JoinResult{}, fmt.Errorf("load state: %w", err)
	} else {
		priv, err = wgkey.GeneratePrivate()
		if err != nil {
			return JoinResult{}, fmt.Errorf("generate private key: %w", err)
		}
	}
	pub := priv.Public()

	cs, err := clusterkey.Parse(tok.Secret)
	if err != nil {
		return JoinResult{}, fmt.Errorf("parse cluster secret: %w", err)
	}
	psk, err := handshake.DerivePSK(cs[:])
	if err != nil {
		return JoinResult{}, fmt.Errorf("derive psk: %w", err)
	}
	seedPub, err := base64.StdEncoding.DecodeString(tok.SeedPubKey)
	if err != nil {
		return JoinResult{}, fmt.Errorf("parse seed pubkey: %w", err)
	}
	if len(seedPub) != 32 {
		return JoinResult{}, fmt.Errorf("seed pubkey wrong size: %d", len(seedPub))
	}

	resp, err := bootstrap.Join(tok.SeedEndpoint, psk, priv, pub, seedPub, proto.HelloRequest{
		Version:   proto.ProtoVersion,
		Label:     p.Label,
		PublicKey: pub.String(),
		Endpoint:  p.PublicEndpoint,
	})
	if err != nil {
		return JoinResult{}, err
	}
	if resp.Status != "ok" {
		return JoinResult{}, fmt.Errorf("seed rejected: %s", resp.Error)
	}

	peers := make([]state.Peer, 0, len(resp.Peers))
	for _, pi := range resp.Peers {
		peers = append(peers, state.Peer{
			Label:     pi.Label,
			PublicKey: pi.PublicKey,
			Endpoint:  pi.Endpoint,
			NodeIP:    pi.NodeIP,
			IsSeed:    pi.IsSeed,
		})
	}

	// resume без --public-endpoint — сохраняем прежний listen-port (seed помнит
	// объявленный ранее endpoint, порт должен совпадать).
	ownPort := p.OwnListenPort
	if ownPort == 0 && existing != nil {
		ownPort = existing.ListenPort
	}

	newState := &state.State{
		NodeLabel:     p.Label,
		ClusterSecret: tok.Secret,
		AwgParams:     resp.AwgParams,
		NetworkCIDR:   resp.NetworkCIDR,
		PrivateKey:    priv.String(),
		PublicKey:     pub.String(),
		NodeIP:        resp.YourIP,
		ListenPort:    ownPort,
		IsSeed:        false,
		Peers:         peers,
	}

	if existing != nil {
		// resume — атомарная замена через Store (RMW-safe при живом демоне).
		newState.CreatedAt = existing.CreatedAt
		if _, err := store.Update(func(s *state.State) error {
			*s = *newState
			return nil
		}); err != nil {
			return JoinResult{}, fmt.Errorf("save state: %w", err)
		}
	} else {
		// первый join — файла ещё нет, демон не запущен, прямой Save.
		if err := newState.Save(p.StateFile); err != nil {
			return JoinResult{}, fmt.Errorf("save state: %w", err)
		}
	}

	// Фактический endpoint нашей ноды из ответа seed'а.
	endpoint := ""
	for _, pr := range peers {
		if pr.PublicKey == pub.String() {
			endpoint = pr.Endpoint
			break
		}
	}

	return JoinResult{
		Label:        p.Label,
		NetworkCIDR:  resp.NetworkCIDR,
		NodeIP:       resp.YourIP,
		Endpoint:     endpoint,
		PublicKey:    pub.String(),
		SeedEndpoint: tok.SeedEndpoint,
		PeersKnown:   len(peers),
		StateFile:    p.StateFile,
	}, nil
}
