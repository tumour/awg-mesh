package node

import (
	"fmt"
	"os"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/clusterkey"
	"github.com/tumour/awg-mesh/internal/jointoken"
	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// InitParams — параметры инициализации новой mesh-сети (seed-нода).
type InitParams struct {
	Label          string
	ListenPort     int    // WG listen-port (из --listen)
	PublicEndpoint string // host:port, обязателен для seed
	CIDR           string
	StateFile      string
}

// InitResult — итог Init: куда записано и токен для онбординга нод.
type InitResult struct {
	Label          string
	CIDR           string
	NodeIP         string
	PublicEndpoint string
	StateFile      string
	JoinToken      string
}

// Init инициализирует новую mesh-сеть: генерит cluster-secret, awg-params,
// keypair, выделяет seed-IP, пишет state.json (is_seed=true) и собирает
// join-token. Чистая операция — без CLI/печати/firewall/autostart (их делает
// caller). Импортируема и тестируема; вебморда дёргает её напрямую.
//
// Идемпотентность: отказывается, если state-file уже существует (защита от
// перезаписи ключей).
func Init(p InitParams) (InitResult, error) {
	if p.Label == "" {
		return InitResult{}, fmt.Errorf("label is required")
	}
	if p.PublicEndpoint == "" {
		return InitResult{}, fmt.Errorf("public endpoint is required (e.g. 45.146.165.227:51820)")
	}
	if _, err := os.Stat(p.StateFile); err == nil {
		return InitResult{}, fmt.Errorf("state file %s already exists; delete it manually to re-init", p.StateFile)
	}

	cs, err := clusterkey.Generate()
	if err != nil {
		return InitResult{}, fmt.Errorf("cluster-secret: %w", err)
	}
	params, err := awgparams.Generate()
	if err != nil {
		return InitResult{}, fmt.Errorf("awg-params: %w", err)
	}
	priv, err := wgkey.GeneratePrivate()
	if err != nil {
		return InitResult{}, fmt.Errorf("wg keypair: %w", err)
	}
	pub := priv.Public()

	hubIP, err := mesh.FirstUsableIP(p.CIDR)
	if err != nil {
		return InitResult{}, fmt.Errorf("parse cidr: %w", err)
	}

	s := &state.State{
		NodeLabel:     p.Label,
		ClusterSecret: cs.String(),
		AwgParams:     params,
		LocalObf:      awgparams.DefaultLocalObf(),
		NetworkCIDR:   p.CIDR,
		PrivateKey:    priv.String(),
		PublicKey:     pub.String(),
		NodeIP:        hubIP,
		ListenPort:    p.ListenPort,
		IsSeed:        true,
		Peers: []state.Peer{{
			Label:     p.Label,
			PublicKey: pub.String(),
			Endpoint:  p.PublicEndpoint,
			NodeIP:    hubIP,
			IsSeed:    true,
		}},
	}
	if err := s.Save(p.StateFile); err != nil {
		return InitResult{}, fmt.Errorf("save state: %w", err)
	}

	// Bootstrap-token = base64url(JSON{secret, seed_pubkey, seed_endpoint}).
	token, err := jointoken.Encode(jointoken.Token{
		Secret:       cs.String(),
		SeedPubKey:   pub.String(),
		SeedEndpoint: p.PublicEndpoint,
	})
	if err != nil {
		return InitResult{}, fmt.Errorf("encode join-token: %w", err)
	}

	return InitResult{
		Label:          p.Label,
		CIDR:           p.CIDR,
		NodeIP:         hubIP,
		PublicEndpoint: p.PublicEndpoint,
		StateFile:      p.StateFile,
		JoinToken:      token,
	}, nil
}
