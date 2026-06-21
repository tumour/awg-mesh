// Package node — оркестрация рантайма демона meshd: поднимает AmneziaWG-device,
// конфигурирует peers, запускает gossip и (на seed'е) bootstrap-listener,
// обрабатывает graceful shutdown по ctx. Доменные решения берёт из internal/mesh,
// транспорт — из internal/bootstrap и internal/gossip.
//
// ОС-зависимые вызовы (cleanup/down TUN-интерфейса) пока через `ip` (Linux) —
// помечены TODO и будут вынесены за build-tags при добавлении кроссплатформы.
package node

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	awgdev "github.com/amnezia-vpn/amneziawg-go/device"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/bootstrap"
	"github.com/tumour/awg-mesh/internal/clusterkey"
	"github.com/tumour/awg-mesh/internal/gossip"
	"github.com/tumour/awg-mesh/internal/handshake"
	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wg"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// Device — то, что нужно оркестратору от wg-устройства. Узкий интерфейс (его
// удовлетворяет *wg.Device) открывает тестирование run-flow без реального TUN/root.
type Device interface {
	Configure(priv wgkey.Private, awgp awgparams.Params, peers []state.Peer, selfPubKey wgkey.Public) error
	UpdatePeer(p state.Peer) error
	Up() error
	AssignIP(cidr string) error
	Name() string
	Close()
}

// Options — параметры запуска демона.
type Options struct {
	StateFile      string
	Interface      string
	Verbose        bool
	GossipInterval time.Duration
	// FirewallWarn (опц.) вызывается после поднятия интерфейса с его именем — cmd
	// передаёт сюда host-integration проверку (UFW), вне домена и оркестрации.
	FirewallWarn func(ifaceName string)
}

// Run — главный foreground-цикл демона. Завершается при отмене ctx (SIGTERM/INT).
// Идемпотентен: device пересоздаётся каждый запуск, peers применяются заново,
// state на диске — source of truth.
func Run(ctx context.Context, opts Options) error {
	store := state.NewStore(opts.StateFile)
	s, err := store.Read()
	if err != nil {
		return err
	}

	priv, err := wgkey.ParsePrivate(s.PrivateKey)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	pub := priv.Public()

	logLevel := awgdev.LogLevelError
	if opts.Verbose {
		logLevel = awgdev.LogLevelVerbose
	}

	// Фиксированный UDP-порт — только если к нам можно постучаться (seed или нода
	// с объявленным endpoint'ом). За NAT — ephemeral-порт (нода всегда initiator).
	listenPort := 0
	if s.IsSeed || mesh.SelfEndpoint(s) != "" {
		listenPort = s.ListenPort
	}

	cleanupStaleInterface(opts.Interface)

	device, err := wg.New(opts.Interface, listenPort, logLevel)
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
	if err := device.AssignIP(s.NodeIP + cidrSuffix(s.NetworkCIDR)); err != nil {
		return fmt.Errorf("assign ip: %w", err)
	}
	log.Printf("meshd run: %s up, mesh-ip=%s", device.Name(), s.NodeIP)

	if opts.FirewallWarn != nil {
		opts.FirewallWarn(device.Name())
	}

	// На seed'е — встроенный bootstrap-listener; после каждого join'а пушим
	// свежий peer-list в live device (incremental UAPI).
	if s.IsSeed {
		psk, err := derivePSK(s)
		if err != nil {
			return err
		}
		addr := fmt.Sprintf(":%d", s.ListenPort)
		go func() {
			if err := bootstrap.Serve(ctx, addr, store, priv, pub, psk, func() {
				pushPeers(store, device, pub)
			}); err != nil {
				log.Printf("bootstrap: %v", err)
			}
		}()
	}

	// Gossip-server (отдаёт peer-list, слушает только mesh-IP) + client (pull).
	gossipSrv := gossip.NewServer(s.NodeIP, gossip.DefaultPort, store)
	go func() {
		if err := gossipSrv.Start(ctx); err != nil {
			log.Printf("gossip server: %v", err)
		}
	}()

	if opts.GossipInterval > 0 {
		gc := gossip.NewClient(store, pub.String(), opts.GossipInterval,
			gossip.DefaultPort, func(newPeers []state.Peer) {
				for _, p := range newPeers {
					if err := device.UpdatePeer(p); err != nil {
						log.Printf("gossip: push %s to device: %v", p.Label, err)
					}
				}
			})
		go gc.Run(ctx)
	}

	log.Printf("meshd run: ready, waiting for signals")
	<-ctx.Done()
	log.Printf("meshd run: received signal, shutting down")

	downInterface(device.Name())
	return nil
}

// pushPeers — добавить/обновить всех peers из state в running device (idempotent).
// Вызывается после bootstrap-join'а. Принимает Device-интерфейс (тестируемо).
func pushPeers(store *state.Store, dev Device, selfPub wgkey.Public) {
	s, err := store.Read()
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

func derivePSK(s *state.State) ([]byte, error) {
	cs, err := clusterkey.Parse(s.ClusterSecret)
	if err != nil {
		return nil, fmt.Errorf("parse cluster secret: %w", err)
	}
	psk, err := handshake.DerivePSK(cs[:])
	if err != nil {
		return nil, fmt.Errorf("derive psk: %w", err)
	}
	return psk, nil
}

// cidrSuffix вытаскивает "/24" из "100.64.0.0/24".
func cidrSuffix(cidr string) string {
	if idx := strings.IndexByte(cidr, '/'); idx >= 0 {
		return cidr[idx:]
	}
	return "/24"
}

// --- ОС-зависимое (Linux, exec ip). TODO: вынести за build-tags (кроссплатформа). ---

// cleanupStaleInterface удаляет залежавшийся TUN от crash'нувшего прошлого
// запуска, иначе tun.CreateTUN зафейлит. «Cannot find device» — норма (его нет).
func cleanupStaleInterface(iface string) {
	if out, err := exec.Command("ip", "link", "delete", "dev", iface).CombinedOutput(); err != nil {
		if !strings.Contains(string(out), "Cannot find device") {
			log.Printf("warn: cleanup stale interface %s: %v: %s",
				iface, err, strings.TrimSpace(string(out)))
		}
	}
}

func downInterface(iface string) {
	if out, err := exec.Command("ip", "link", "set", "down", "dev", iface).CombinedOutput(); err != nil {
		log.Printf("warn: ip link down: %v: %s", err, strings.TrimSpace(string(out)))
	}
}
