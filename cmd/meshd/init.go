package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/clusterkey"
	"github.com/tumour/awg-mesh/internal/jointoken"
	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// cmdInit — инициализация новой mesh-сети.
//
// Генерируем: cluster-secret, AmneziaWG-параметры, наш keypair, network CIDR.
// Пишем state.json с ролью is_seed=true. Печатаем join-команду для других нод.
//
// Идемпотентность: отказывается работать если state-file уже существует
// (иначе можно случайно перезаписать ключи и потерять mesh). Чтобы переинициализировать —
// удалить state-file вручную.
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

	if *label == "" {
		return fmt.Errorf("--label is required")
	}
	if err := validateUFWMode(*ufwMode); err != nil {
		return err
	}
	if *publicEndpoint == "" {
		return fmt.Errorf("--public-endpoint is required (e.g. --public-endpoint 45.146.165.227:51820)")
	}

	// Не перезаписываем существующий state — это опасно (потеря ключей).
	if _, err := os.Stat(*stateFlag); err == nil {
		return fmt.Errorf("state file %s already exists; delete it manually to re-init", *stateFlag)
	}

	cs, err := clusterkey.Generate()
	if err != nil {
		return fmt.Errorf("cluster-secret: %w", err)
	}
	params, err := awgparams.Generate()
	if err != nil {
		return fmt.Errorf("awg-params: %w", err)
	}
	priv, err := wgkey.GeneratePrivate()
	if err != nil {
		return fmt.Errorf("wg keypair: %w", err)
	}
	pub := priv.Public()

	// IP первой ноды = первый usable в CIDR. Для /24 это <network>.1.
	// Парсить CIDR полноценно будем когда добавятся другие ноды; для init этого хватит.
	hubIP, err := mesh.FirstUsableIP(*cidr)
	if err != nil {
		return fmt.Errorf("parse cidr: %w", err)
	}

	s := &state.State{
		Version:       1,
		NodeLabel:     *label,
		ClusterSecret: cs.String(),
		AwgParams:     params,
		NetworkCIDR:   *cidr,
		PrivateKey:    priv.String(),
		PublicKey:     pub.String(),
		NodeIP:        hubIP,
		ListenPort:    parsePort(*listenAddr),
		IsSeed:        true,
		Peers: []state.Peer{
			{
				Label:     *label,
				PublicKey: pub.String(),
				Endpoint:  *publicEndpoint,
				NodeIP:    hubIP,
				IsSeed:    true,
			},
		},
	}

	if err := s.Save(*stateFlag); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// Bootstrap-token = base64url(JSON{secret, seed_pubkey, seed_endpoint}).
	// Один параметр для копипасты вместо трёх — UX-фишка Tailscale auth-keys.
	token, err := jointoken.Encode(jointoken.Token{
		Secret:       cs.String(),
		SeedPubKey:   pub.String(),
		SeedEndpoint: *publicEndpoint,
	})
	if err != nil {
		return fmt.Errorf("encode join-token: %w", err)
	}

	fmt.Printf(`✓ mesh initialized

  label:            %s
  network:          %s
  node ip:          %s
  public endpoint:  %s
  state file:       %s

`, *label, *cidr, hubIP, *publicEndpoint, *stateFlag)

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
`, token)

	return nil
}
