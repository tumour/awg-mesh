// Package node — оркестрация рантайма демона meshd: поднимает AmneziaWG-device,
// конфигурирует peers, запускает gossip и (на seed'е) bootstrap-listener,
// обрабатывает graceful shutdown по ctx. Доменные решения берёт из internal/mesh,
// транспорт — из internal/bootstrap и internal/gossip.
//
// ОС-зависимая настройка линка (адрес, up/down/delete) — за интерфейсом wg.Linker
// (build-tags); сам Run платформо-независим и тестируем через Options.NewDevice/Linker.
package node

import (
	"context"
	"fmt"
	"log/slog"
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

// Device — то, что нужно оркестратору от userspace wg-устройства (kernel-side
// настройку линка делает Linker). Узкий интерфейс — его удовлетворяет *wg.Device;
// вместе с Options.NewDevice/Linker позволяет прогнать run-flow с фейками, без
// реального TUN/root.
type Device interface {
	Configure(priv wgkey.Private, awgp awgparams.Params, localObf awgparams.LocalObf, peers []state.Peer, selfPubKey wgkey.Public) error
	ApplyParams(awgp awgparams.Params) error
	UpdatePeer(p state.Peer) error
	Up() error
	Name() string
	Close()
}

// Options — параметры запуска демона.
type Options struct {
	StateFile      string
	Interface      string
	Verbose        bool
	GossipInterval time.Duration
	// FlipInterval — как часто проверять, не пора ли применить запланированную
	// flag-day-смену params (0 → дефолт). Применение синхронно во всех нодах по
	// Pending.ApplyAt, поэтому интервал должен быть заметно меньше grace.
	FlipInterval time.Duration
	// Logger (опц., nil → slog.Default()) — инъектируемый структурный логгер.
	// Демон не пишет в глобальный log: embeddable из вебморды/LuCI со своим sink'ом.
	Logger *slog.Logger
	// NewDevice/Linker (опц., nil → wg.New / wg.DefaultLinker) — фабрика userspace-
	// устройства и порт настройки линка. Инъектируемы → run-flow тестируем с фейками.
	NewDevice func(name string, listenPort, mtu, logLevel int) (Device, error)
	Linker    wg.Linker
	// FirewallWarn (опц.) вызывается после поднятия интерфейса с его именем — cmd
	// передаёт сюда host-integration проверку (UFW), вне домена и оркестрации.
	FirewallWarn func(ifaceName string)
}

// Run — главный foreground-цикл демона. Завершается при отмене ctx (SIGTERM/INT).
// Идемпотентен: device пересоздаётся каждый запуск, peers применяются заново,
// state на диске — source of truth.
func Run(ctx context.Context, opts Options) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	newDevice := opts.NewDevice
	if newDevice == nil {
		newDevice = func(name string, listenPort, mtu, logLevel int) (Device, error) {
			return wg.New(name, listenPort, mtu, logLevel)
		}
	}
	linker := opts.Linker
	if linker == nil {
		linker = wg.DefaultLinker()
	}
	flipInterval := opts.FlipInterval
	if flipInterval == 0 {
		flipInterval = 10 * time.Second
	}

	store := state.NewStore(opts.StateFile)
	s, err := store.Read()
	if err != nil {
		return err
	}

	// Если state дочитан из старой схемы (Load оставляет прежнюю Version, Save
	// проставит текущую) — разово перепишем файл в актуальный формат. Иначе диск
	// застрянет в старой схеме и будет мигрироваться при каждом старте, а после
	// удаления legacy-чтения в будущей версии — перестанет читаться вовсе.
	if s.Version < state.CurrentVersion {
		from := s.Version
		if migrated, err := store.Update(func(*state.State) error { return nil }); err != nil {
			logger.Warn("rewrite migrated state to current schema failed", "err", err)
		} else {
			s = migrated
			logger.Info("state migrated to current schema", "from", from, "to", state.CurrentVersion)
		}
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

	// Чистим залежавшийся TUN от прошлого crash'а (идемпотентно: нет линка — не ошибка).
	if err := linker.Delete(opts.Interface); err != nil {
		logger.Warn("cleanup stale interface failed", "iface", opts.Interface, "err", err)
	}

	// MTU awg0 с учётом AWG-overhead (s4-паддинг на каждый data-пакет) —
	// иначе крупный TCP на путях с path-MTU < 1500 уходит в PMTU-блэкхол.
	mtu := wg.TunMTU(s.AwgParams.S4)

	device, err := newDevice(opts.Interface, listenPort, mtu, logLevel)
	if err != nil {
		return fmt.Errorf("create wg device: %w", err)
	}
	defer device.Close()

	logger.Info("interface created", "iface", device.Name(),
		"mesh_ip", s.NodeIP, "peers", len(s.Peers), "seed", s.IsSeed, "mtu", mtu)

	if err := device.Configure(priv, s.AwgParams, s.LocalObf, s.Peers, pub); err != nil {
		return fmt.Errorf("configure device: %w", err)
	}
	if err := device.Up(); err != nil {
		return fmt.Errorf("bring device up: %w", err)
	}
	// Kernel-side: назначить mesh-IP и поднять линк (userspace-up уже сделан выше).
	if err := linker.AddIP(device.Name(), s.NodeIP+cidrSuffix(s.NetworkCIDR)); err != nil {
		return fmt.Errorf("assign ip: %w", err)
	}
	if err := linker.SetUp(device.Name()); err != nil {
		return fmt.Errorf("link up: %w", err)
	}
	logger.Info("interface up", "iface", device.Name(), "mesh_ip", s.NodeIP)

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
				pushPeers(store, device, pub, logger)
			}, logger); err != nil {
				logger.Error("bootstrap listener stopped", "err", err)
			}
		}()
	}

	// Gossip-server (отдаёт peer-list, слушает только mesh-IP) + client (pull).
	gossipSrv := gossip.NewServer(s.NodeIP, gossip.DefaultPort, store, logger)
	go func() {
		if err := gossipSrv.Start(ctx); err != nil {
			logger.Error("gossip server stopped", "err", err)
		}
	}()

	if opts.GossipInterval > 0 {
		gc := gossip.NewClient(store, pub.String(), opts.GossipInterval,
			gossip.DefaultPort, func(newPeers []state.Peer) {
				for _, p := range newPeers {
					if err := device.UpdatePeer(p); err != nil {
						logger.Warn("push gossip peer to device failed", "peer", p.Label, "err", err)
					}
				}
			}, logger)
		go gc.Run(ctx)
	}

	// flag-day-смена params: применяем Pending в назначенный ApplyAt (синхронно
	// со всей сетью). reconfigure на лету, без пересоздания awg0.
	go runParamFlip(ctx, store, device, flipInterval, logger)

	// seed назначает ApplyAt анонсированному Pending только когда ВСЕ ноды
	// подтвердили его приём (ack-then-commit) — иначе flip не стартует и сеть
	// остаётся на старом наборе целиком (ни одну ноду не теряем).
	if s.IsSeed {
		go runParamCommit(ctx, store, gossipSrv, pub.String(), flipInterval, logger)
	}

	logger.Info("ready, waiting for signals")
	<-ctx.Done()
	logger.Info("received signal, shutting down")

	// Линк явно НЕ опускаем: awg0 — userspace-TUN, defer device.Close() выше
	// удаляет интерфейс целиком (адрес уходит вместе с ним), так что SetDown
	// здесь избыточен. Под systemd он ещё и шумел ложным WARN'ом: KillMode=
	// control-group шлёт SIGTERM всей cgroup → дочерний `ip link set down`
	// гибнет с «signal: terminated» (на procd-роутерах этого нет). На связность
	// mesh не влияло — только лишняя строка в логе на каждый рестарт.
	return nil
}

// pushPeers — добавить/обновить всех peers из state в running device (idempotent).
// Вызывается после bootstrap-join'а. Принимает Device-интерфейс (тестируемо).
func pushPeers(store *state.Store, dev Device, selfPub wgkey.Public, logger *slog.Logger) {
	s, err := store.Read()
	if err != nil {
		logger.Error("push-peers: reload state failed", "err", err)
		return
	}
	for _, p := range s.Peers {
		if p.PublicKey == selfPub.String() {
			continue
		}
		if err := dev.UpdatePeer(p); err != nil {
			logger.Warn("push-peers: update peer failed", "peer", p.Label, "err", err)
		}
	}
}

// runParamFlip периодически применяет запланированную flag-day-смену params,
// когда наступает её ApplyAt. Завершается по ctx.
func runParamFlip(ctx context.Context, store *state.Store, dev Device, interval time.Duration, logger *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			applyDueParams(store, dev, logger)
		}
	}
}

// applyDueParams применяет Pending, если наступил его ApplyAt: фиксирует новый
// набор в state (под Store.Update, RMW-safe) и reconfigure'ит device на лету.
// Решение «пора ли» — доменное (mesh.PendingDue); здесь только применение.
// Идемпотентно: не пора / нет Pending → ErrNoChange (диск не трогаем).
func applyDueParams(store *state.Store, dev Device, logger *slog.Logger) {
	var applied awgparams.Params
	var version uint64
	var did bool // выставляется в fn только при реальном применении (Update гасит ErrNoChange в nil)
	if _, err := store.Update(func(s *state.State) error {
		if !mesh.PendingDue(s.Pending, time.Now().UTC()) {
			return state.ErrNoChange
		}
		applied, version, did = s.Pending.Params, s.Pending.Version, true
		s.AwgParams = applied
		s.ParamsVersion = version
		s.Pending = nil
		return nil
	}); err != nil {
		logger.Error("flip: persist params failed", "err", err)
		return
	}
	if !did {
		return // не пора / нет Pending
	}
	if err := dev.ApplyParams(applied); err != nil {
		// Слой откат/watchdog — отдельно; пока фиксируем. Связь восстановят
		// рехендшейки, если остальные ноды применили тот же набор синхронно.
		logger.Error("flip: apply params to device failed", "version", version, "err", err)
		return
	}
	logger.Info("flag-day params applied", "version", version)
}

// commitGrace — фора между назначением ApplyAt (когда все ноды подтвердили
// приём Pending) и самим flip: чтобы commit (ApplyAt) успел разойтись по gossip
// до момента применения. Короткая — все уже знают сам Pending, осталось ApplyAt.
const commitGrace = 30 * time.Second

// runParamCommit (seed-only) назначает ApplyAt анонсированному Pending, когда
// ВСЕ ноды подтвердили его приём. Завершается по ctx.
func runParamCommit(ctx context.Context, store *state.Store, acks interface{ Acks() map[string]uint64 }, selfPub string, interval time.Duration, logger *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			commitIfAllAcked(store, acks.Acks(), selfPub, logger)
		}
	}
}

// commitIfAllAcked назначает ApplyAt, если есть анонсированный Pending и все
// пиры подтвердили его приём (домен mesh.AllPeersAcked/CommitPending). Идемпотентно.
func commitIfAllAcked(store *state.Store, acks map[string]uint64, selfPub string, logger *slog.Logger) {
	var committed *state.PendingParams
	if _, err := store.Update(func(s *state.State) error {
		if s.Pending == nil || !s.Pending.ApplyAt.IsZero() {
			return state.ErrNoChange // нет анонса / уже закоммичен
		}
		if !mesh.AllPeersAcked(s.Peers, selfPub, acks, s.Pending.Version) {
			return state.ErrNoChange // не все подтвердили — ждём
		}
		mesh.CommitPending(s.Pending, time.Now().UTC(), commitGrace)
		committed = s.Pending
		return nil
	}); err != nil {
		logger.Error("commit: persist failed", "err", err)
		return
	}
	if committed != nil {
		logger.Info("flag-day committed (all nodes acked)", "version", committed.Version, "apply_at", committed.ApplyAt)
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
