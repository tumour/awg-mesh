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
	"os"
	"path/filepath"
	"strings"
	"time"

	awgdev "github.com/amnezia-vpn/amneziawg-go/device"

	"github.com/tumour/awg-mesh/internal/api"
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
	ApplyObf(localObf awgparams.LocalObf) error
	UpdatePeer(p state.Peer) error
	RemovePeer(pubkeyBase64 string) error
	PeerStats() ([]wg.PeerStat, error)
	Up() error
	Name() string
	Close()
}

// staticWebDir — каталог web-морды, если он реально установлен (есть index.html).
// Иначе "" → API поднимается без статики: нода без web-пакета не отдаёт битый UI.
func staticWebDir() string {
	if _, err := os.Stat(filepath.Join(api.DefaultWebDir, "index.html")); err != nil {
		return ""
	}
	return api.DefaultWebDir
}

// deviceLiveStats — адаптер device → api.LiveStatsFunc: читает live-статистику
// wg-device и конвертирует в доменный вход mesh.PeerLive (домен про UAPI/wg не знает).
func deviceLiveStats(d Device) api.LiveStatsFunc {
	return func() (map[string]mesh.PeerLive, error) {
		stats, err := d.PeerStats()
		if err != nil {
			return nil, err
		}
		live := make(map[string]mesh.PeerLive, len(stats))
		for _, st := range stats {
			live[st.PublicKey] = mesh.PeerLive{LastHandshake: st.LastHandshake}
		}
		return live, nil
	}
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

	// Startup: вычистить отозванных (tombstone) из Peers ДО Configure — иначе рестарт
	// демона воскресил бы revoked на свежем device (Configure пушит peer-list без
	// фильтра tombstones). Применение к РАБОТАЮЩЕМУ device в рантайме — reap-loop ниже.
	if pruned, err := store.Update(func(st *state.State) error {
		kept, removed := mesh.ApplyTombstones(st.Peers, st.Tombstones, st.PublicKey)
		if len(removed) == 0 {
			return state.ErrNoChange
		}
		st.Peers = kept
		return nil
	}); err != nil {
		logger.Warn("startup prune revoked peers failed", "err", err)
	} else {
		s = pruned
	}

	// Backfill дефолтной обфускации (I1 = QUIC-мимик): мигрированные с v1 ноды имеют
	// ПУСТОЙ local_obf (миграция оставляла его пустым ради wire-identical апгрейда).
	// Без I1 у инициатора нет маскировки старта потока → stateful-DPI душит сессию.
	// I1 initiator-local (получатель игнорирует) → backfill безопасен для уже живых
	// туннелей. Заданную вручную обфускацию не трогаем (ErrNoChange при гонке).
	if s.LocalObf.IsEmpty() {
		if filled, err := store.Update(func(st *state.State) error {
			if !st.LocalObf.IsEmpty() {
				return state.ErrNoChange
			}
			st.LocalObf = awgparams.DefaultLocalObf()
			return nil
		}); err != nil {
			logger.Warn("backfill default obfuscation failed", "err", err)
		} else {
			s = filled
			logger.Info("default obfuscation backfilled (I1 QUIC-mimic) into empty local_obf")
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

	// Read-only control-API для web-морды — ТОЛЬКО на seed (web живёт на seed),
	// слушает только mesh-IP как gossip. Тонкий бинарь на роутерах его не поднимает.
	if s.IsSeed {
		apiSrv := api.NewServer(s.NodeIP, api.DefaultPort, store, deviceLiveStats(device), staticWebDir(), logger)
		go func() {
			if err := apiSrv.Start(ctx); err != nil {
				logger.Error("api server stopped", "err", err)
			}
		}()
	}

	if opts.GossipInterval > 0 {
		gc := gossip.NewClient(store, pub.String(), opts.GossipInterval,
			gossip.DefaultPort,
			func(newPeers []state.Peer) {
				for _, p := range newPeers {
					if err := device.UpdatePeer(p); err != nil {
						logger.Warn("push gossip peer to device failed", "peer", p.Label, "err", err)
					}
				}
			},
			func(removedPeers []state.Peer) {
				// Отозванных (tombstone) снимаем с wg-device на лету — без рестарта.
				for _, p := range removedPeers {
					if err := device.RemovePeer(p.PublicKey); err != nil {
						logger.Warn("remove revoked peer from device failed", "peer", p.Label, "err", err)
					}
				}
			},
			logger)
		go gc.Run(ctx)
	}

	// flag-day-смена params: применяем Pending в назначенный ApplyAt (синхронно со
	// всей сетью), reconfigure на лету. Тайминги привязаны к gossip-интервалу:
	//  • commitGrace (= maxStale flip'а) — фора, за которую committed ApplyAt должен
	//    разойтись по gossip ДО применения;
	//  • abortMargin — запас перед ApplyAt, до которого seed решает abort, если не все
	//    подтвердили приём ApplyAt (commitGrace > abortMargin, чтобы зазор был).
	// flipper держит in-memory seenAt: когда нода ВПЕРВЫЕ увидела committed ApplyAt —
	// флипаем только если держим его достаточно давно (любой abort бы успел дойти).
	commitGrace := commitGraceFor(opts.GossipInterval)
	abortMargin := abortMarginFor(opts.GossipInterval)
	flipper := &paramFlipper{store: store, dev: device, abortMargin: abortMargin, maxStale: commitGrace, log: logger}
	go runParamFlip(ctx, flipper, flipInterval)

	// revoke/leave: снимаем отозванных с device НЕЗАВИСИМО от gossip-pull. Иначе при
	// offline-таргете / в 2-нодовой сети / при NAT-leave (у leaver'а нет endpoint,
	// seed не имеет gossip-кандидата) отозванный остался бы живым peer'ом на device.
	go runTombstoneReap(ctx, store, device, flipInterval, logger)

	// obf-обход: применяем присланный seed'ом per-node I1 на лету (reconciler по ObfVersion).
	// Начальная версия = boot-конфиг (Configure уже применил текущий LocalObf при подъёме awg0).
	go runObfApply(ctx, newObfApplier(store, device, s.ObfVersion, logger), flipInterval)

	// seed: flag-day раздаётся active-push'ем — seed POST'ит Pending каждой ноде и
	// собирает ack ПРЯМО из ответа (а не пассивным pull'ом, дававшим livelock+strand).
	// На тех же ack'ах: commit-гейт назначает ApplyAt, когда все подтвердили АНОНС;
	// arm/abort-гейт отменяет flip (переанонс v+1), если к дедлайну не все подтвердили
	// приём ApplyAt — тогда не флипает никто, сеть цела, ретрай (гарантия «ноду не теряем»).
	if s.IsSeed {
		go runParamPush(ctx, newSeedParamPusher(store, gossip.DefaultPort, pub.String(), commitGrace, abortMargin, logger), flipInterval)
		// obf-обход: seed активно раздаёт каждой ноде её per-node I1 (генерит из
		// ObfPolicy.SNI) и ретраит до подтверждения всех. I1 не рвёт туннель → strand невозможен.
		go runObfPush(ctx, newSeedObfPusher(store, gossip.DefaultPort, pub.String(), logger), flipInterval)
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

// paramFlipper применяет запланированную flag-day-смену params в её ApplyAt. Держит
// in-memory, когда ВПЕРВЫЕ увидел committed ApplyAt текущей версии (seenVer/seenAt):
// флипаем только если держим его не меньше abortMargin — коммит, полученный впритык,
// не применяем, т.к. возможен abort в полёте, который мы ещё не получили (анти-split).
type paramFlipper struct {
	store       *state.Store
	dev         Device
	abortMargin time.Duration
	maxStale    time.Duration
	log         *slog.Logger

	seenVer uint64    // версия committed Pending, который мы наблюдаем (0 = коммита нет)
	seenAt  time.Time // когда впервые увидели его ApplyAt
}

// runParamFlip тикает flipper.tick до отмены ctx.
func runParamFlip(ctx context.Context, f *paramFlipper, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			f.tick(time.Now().UTC())
		}
	}
}

// tick: обновить наблюдение за committed ApplyAt, затем применить, если пора и держим
// достаточно давно (mesh.ShouldFire).
func (f *paramFlipper) tick(now time.Time) {
	s, err := f.store.Read()
	if err != nil {
		f.log.Warn("flip: read state failed", "err", err)
		return
	}
	f.observe(s.Pending, now)
	f.applyIfDue(now)
}

// observe запоминает момент первого наблюдения committed ApplyAt текущей версии.
// Анонс (ApplyAt=0) или отсутствие Pending сбрасывают наблюдение — следующий commit
// (в т.ч. после abort'а на v+1) переснимет seenAt заново.
func (f *paramFlipper) observe(p *state.PendingParams, now time.Time) {
	if p == nil || p.ApplyAt.IsZero() {
		f.seenVer = 0
		return
	}
	if p.Version != f.seenVer {
		f.seenVer, f.seenAt = p.Version, now
	}
}

// applyIfDue фиксирует новый набор в state (под Store.Update, RMW-safe) и reconfigure'ит
// device на лету, если mesh.ShouldFire (due + держим ApplyAt дольше abortMargin).
// Идемпотентно: не пора / нет Pending → ErrNoChange (диск не трогаем).
func (f *paramFlipper) applyIfDue(now time.Time) {
	var applied awgparams.Params
	var version uint64
	var did bool // выставляется в fn только при реальном применении (Update гасит ErrNoChange в nil)
	if _, err := f.store.Update(func(s *state.State) error {
		if !mesh.ShouldFire(s.Pending, f.seenAtFor(s.Pending), now, f.abortMargin, f.maxStale) {
			return state.ErrNoChange
		}
		applied, version, did = s.Pending.Params, s.Pending.Version, true
		s.AwgParams = applied
		s.ParamsVersion = version
		s.Pending = nil
		return nil
	}); err != nil {
		f.log.Error("flip: persist params failed", "err", err)
		return
	}
	if !did {
		return // не пора / держим коммит недостаточно давно / нет Pending
	}
	if err := f.dev.ApplyParams(applied); err != nil {
		// Слой откат/watchdog — отдельно; пока фиксируем. Связь восстановят
		// рехендшейки, если остальные ноды применили тот же набор синхронно.
		f.log.Error("flip: apply params to device failed", "version", version, "err", err)
		return
	}
	f.log.Info("flag-day params applied", "version", version)
}

// seenAtFor — наше время первого наблюдения committed ApplyAt, но только если это та же
// версия, что мы наблюдали (иначе нулевое: гонка observe↔Update, версия сменилась →
// ShouldFire откажет, переснимем на следующем тике).
func (f *paramFlipper) seenAtFor(p *state.PendingParams) time.Time {
	if p == nil || f.seenVer != p.Version {
		return time.Time{}
	}
	return f.seenAt
}

// runTombstoneReap периодически снимает отозванные ноды с device (см. reapRevoked).
// Завершается по ctx.
func runTombstoneReap(ctx context.Context, store *state.Store, dev Device, interval time.Duration, logger *slog.Logger) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			reapRevoked(store, dev, logger)
		}
	}
}

// reapRevoked снимает отозванные (tombstone) ноды с wg-device и убирает их из
// state.Peers — НЕЗАВИСИМО от gossip-pull. Это страховка для случаев, где doRound
// никогда не доходит до применения: offline-таргет, 2-нодовая сеть, NAT-leave (у
// leaver'а нет endpoint → seed не имеет gossip-кандидата для pull).
//
// Device-снятие идёт по СПИСКУ tombstones, а НЕ по diff'у Peers — так reap работает
// истинным реконсилером: повторяет прошлую неудачу RemovePeer и гасит peer'а,
// которого gossip-гонка (UpdatePeer) могла ре-добавить на device уже после prune'а
// из Peers. RemovePeer для отсутствующего peer'а — дешёвый UAPI no-op, так что
// повтор каждый tick безвреден.
func reapRevoked(store *state.Store, dev Device, logger *slog.Logger) {
	s, err := store.Read()
	if err != nil {
		logger.Warn("reap revoked: read failed", "err", err)
		return
	}
	if len(s.Tombstones) == 0 {
		return
	}

	// 1. Device-reconcile: снять с wg-device каждого отозванного (кроме self —
	//    форж tombstone(self) не должен гасить нас самих, как и в ApplyTombstones).
	for _, t := range s.Tombstones {
		if t.PublicKey == s.PublicKey {
			continue
		}
		if err := dev.RemovePeer(t.PublicKey); err != nil {
			logger.Warn("reap revoked: remove from device failed", "pubkey", mesh.ShortKey(t.PublicKey), "err", err)
		}
	}

	// 2. State-prune: убрать отозванных из Peers (не анонсировать их и не конфигурить
	//    на рестарте). Идемпотентно: уже вычищено → ErrNoChange (диск не трогаем).
	var pruned []state.Peer
	if _, err := store.Update(func(s *state.State) error {
		kept, removed := mesh.ApplyTombstones(s.Peers, s.Tombstones, s.PublicKey)
		if len(removed) == 0 {
			return state.ErrNoChange
		}
		s.Peers = kept
		pruned = removed
		return nil
	}); err != nil {
		logger.Warn("reap revoked: persist failed", "err", err)
		return
	}
	for _, p := range pruned {
		logger.Info("revoked peer pruned from peer-list", "peer", p.Label, "mesh_ip", p.NodeIP)
	}
}

// commitGraceCycles / abortMarginCycles — окна flag-day в gossip-циклах.
// commitGrace — фора от commit до ApplyAt, за которую committed ApplyAt расходится по
// gossip и собираются commit-ack'и. abortMargin — запас перед ApplyAt, до которого
// seed решает abort, если не все подтвердили приём ApplyAt; за этот же запас переанонс
// успевает дойти до committed-нод. Инвариант: commitGrace > abortMargin (нужен зазор,
// чтобы было где собрать commit-ack'и до дедлайна abort'а).
const (
	commitGraceCycles = 4
	abortMarginCycles = 2
)

// commitGraceFor — фора ApplyAt в commitGraceCycles циклов, пол 30с для малых
// интервалов/тестов. См. commitGraceCycles.
func commitGraceFor(gossipInterval time.Duration) time.Duration {
	if g := commitGraceCycles * gossipInterval; g > 30*time.Second {
		return g
	}
	return 30 * time.Second
}

// abortMarginFor — запас перед ApplyAt в abortMarginCycles циклов, пол 15с (< пола
// commitGrace, чтобы инвариант commitGrace > abortMargin держался и на малых интервалах).
func abortMarginFor(gossipInterval time.Duration) time.Duration {
	if m := abortMarginCycles * gossipInterval; m > 15*time.Second {
		return m
	}
	return 15 * time.Second
}

// abortIfStuck (seed-only) отменяет застрявший committed flip: если к дедлайну
// ApplyAt-abortMargin НЕ все подтвердили приём ApplyAt (commit-ack) — переанонсит те же
// params как v+1 с ApplyAt=0. v+1 > v → все принимают (ShouldAdoptPending), сбрасывая
// committed-v → flip v не делает НИКТО, сеть остаётся на старом наборе, цикл с начала.
// Так медленную ноду не теряем: лучше не флипнуть совсем, чем флипнуть подмножество.
func abortIfStuck(store *state.Store, commitAcks map[string]uint64, selfPub string, abortMargin time.Duration, now time.Time, logger *slog.Logger) {
	var aborted *state.PendingParams
	if _, err := store.Update(func(s *state.State) error {
		p := s.Pending
		if p == nil || p.ApplyAt.IsZero() {
			return state.ErrNoChange // нет committed Pending — отменять нечего
		}
		armed := mesh.AllPeersAcked(s.Peers, selfPub, commitAcks, p.Version)
		if !mesh.ShouldAbort(p, now, armed, abortMargin) {
			return state.ErrNoChange // armed (все подтвердили) либо дедлайн ещё не настал
		}
		// Переанонс тех же params как строго новее (v+1) с ApplyAt=0 (announced).
		s.Pending = mesh.NewPending(s.ParamsVersion, p, p.Params)
		aborted = s.Pending
		return nil
	}); err != nil {
		logger.Error("abort: persist failed", "err", err)
		return
	}
	if aborted != nil {
		logger.Warn("flag-day aborted — not all nodes acked the committed ApplyAt; re-announced",
			"version", aborted.Version)
	}
}

// commitIfAllAcked назначает ApplyAt, если есть анонсированный Pending и все
// пиры подтвердили его приём (домен mesh.AllPeersAcked/CommitPending). Идемпотентно.
func commitIfAllAcked(store *state.Store, acks map[string]uint64, selfPub string, grace time.Duration, logger *slog.Logger) {
	var committed *state.PendingParams
	if _, err := store.Update(func(s *state.State) error {
		if s.Pending == nil || !s.Pending.ApplyAt.IsZero() {
			return state.ErrNoChange // нет анонса / уже закоммичен
		}
		if !mesh.AllPeersAcked(s.Peers, selfPub, acks, s.Pending.Version) {
			return state.ErrNoChange // не все подтвердили — ждём
		}
		mesh.CommitPending(s.Pending, time.Now().UTC(), grace)
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
