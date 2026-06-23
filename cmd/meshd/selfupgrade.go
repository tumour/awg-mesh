package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tumour/awg-mesh/internal/gossip"
	"github.com/tumour/awg-mesh/internal/health"
	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/selfupdate"
	"github.com/tumour/awg-mesh/internal/state"
)

// self-upgrade — безопасная замена бинаря на ноде, единственный канал к которой
// сам mesh-туннель. Два риска такого апгрейда и как они закрыты:
//
//  1. Рестарт демона роняет awg0 → рвётся наша же SSH-сессия посреди апгрейда.
//     Лечится detached-запуском (setsid): подмена и рестарт переживают разрыв.
//  2. Новый бинарь не поднимается → теряем ЕДИНСТВЕННЫЙ доступ к ноде.
//     Лечится watchdog'ом: отдельный процесс из СТАРОГО бинаря ждёт возврата
//     связи и, если её нет, откатывает бинарь и рестартует демон.
const (
	// Бэкап и лог — в /tmp (tmpfs/RAM на OpenWrt): переживают рестарт демона и
	// разрыв сессии (ровно окно работы watchdog'а), не занимают дефицитный
	// flash роутера. Ребут их сотрёт — к тому моменту апгрейд уже решён.
	// ОГРАНИЧЕНИЕ: на хостах с noexec-/tmp (харднутый Debian/Hetzner) watchdog из
	// бэкапа не запустится → self-upgrade упадёт на arm watchdog, БЕЗОПАСНО (до
	// подмены, нода цела). Тогда — обычный путь обновления по второму каналу.
	upgradeBackupPath = "/tmp/meshd.upgrade-backup"
	watchdogLogPath   = "/tmp/meshd-watchdog.log"

	defaultHealthGrace   = 20 * time.Second  // дать рестарту устаканиться
	defaultHealthTimeout = 180 * time.Second // ждать возврата связи до отката
	healthProbeInterval  = 10 * time.Second

	// Таймауты применения (защита от зависшего apk/systemctl). apk качает
	// пакет по медленному mesh-каналу — щедро; рестарт init-системы быстрый.
	applyTimeout   = 10 * time.Minute
	restartTimeout = 60 * time.Second
)

// cmdSelfUpgrade — обновить meshd на ноде, единственный канал к которой — сам
// mesh-туннель, с авто-откатом при потере связи. Два режима:
//
//	meshd self-upgrade            — через apk-фид (apk update && apk upgrade meshd)
//	meshd self-upgrade <path>     — заменить бинарь файлом (airgapped/без фида)
//
// Общая защита для обоих: бэкап текущего бинаря + watchdog из СТАРОЙ копии,
// detached-запуск (переживает разрыв SSH при пересоздании awg0), откат бинаря
// если mesh не вернулась. В apk-режиме рестарт делает postupgrade-хук пакета.
func cmdSelfUpgrade(args []string) error {
	fs := flag.NewFlagSet("self-upgrade", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	healthPeer := fs.String("health-peer", "",
		"mesh-IP of a neighbour for the post-upgrade reachability check "+
			"(default: auto-pick the seed or a peer from state)")
	healthTimeout := fs.Duration("health-timeout", defaultHealthTimeout,
		"how long the watchdog waits for mesh connectivity before rolling back")
	grace := fs.Duration("health-grace", defaultHealthGrace,
		"delay before the watchdog starts probing (lets the restart settle)")
	fs.Parse(args)

	if os.Geteuid() != 0 {
		return fmt.Errorf("must run as root (replaces the binary and restarts the daemon)")
	}

	// Режим определяется наличием позиционного аргумента: путь → file-режим,
	// без аргумента → apk-режим (нужен apk в PATH).
	rest := fs.Args()
	useApk := len(rest) == 0
	var newBin string
	if !useApk {
		if len(rest) != 1 {
			return fmt.Errorf("usage: meshd self-upgrade [flags] [<path-to-new-binary>]")
		}
		newBin = rest[0]
	} else if _, err := exec.LookPath("apk"); err != nil {
		return fmt.Errorf("no binary path given and apk not found — pass a path " +
			"(meshd self-upgrade <bin>) or run on an OpenWrt node with the apk feed configured")
	}

	// Куда ставим — путь нашего собственного бинаря (с разворотом симлинков).
	target, err := selfupdate.ResolveSelfPath()
	if err != nil {
		return fmt.Errorf("resolve current binary: %w", err)
	}

	// File-режим: проверяем новый бинарь ДО подмены, пока связь цела.
	var newVer string
	if !useApk {
		fi, err := os.Stat(newBin)
		if err != nil {
			return fmt.Errorf("new binary: %w", err)
		}
		if fi.IsDir() {
			return fmt.Errorf("new binary %q is a directory", newBin)
		}
		// scp мог не сохранить executable-бит — выставляем, иначе version-check
		// не запустится.
		if err := os.Chmod(newBin, 0o755); err != nil {
			return fmt.Errorf("chmod new binary: %w", err)
		}
		// КРИТИЧНО: кривой/чужой/битый бинарь отвалится здесь, до подмены.
		newVer, err = selfupdate.BinaryVersion(newBin)
		if err != nil {
			return fmt.Errorf("new binary failed `version` check (wrong arch or corrupt?): %w", err)
		}
	}

	// State нужен для нашего mesh-IP и выбора health-peer. Нет state — нода
	// не настроена, апгрейдить нечего.
	st, err := state.Load(*stateFlag)
	if err != nil {
		return fmt.Errorf("read state (%s): %w", *stateFlag, err)
	}
	selfIP := st.NodeIP
	peer := *healthPeer
	if peer == "" {
		peer = mesh.PickHealthPeer(st)
	}

	fmt.Printf("current: meshd %s  (%s)\n", version, target)
	if useApk {
		fmt.Println("source:  apk feed (apk update && apk add --latest meshd)")
	} else {
		fmt.Printf("new:     meshd %s  (%s)\n", newVer, newBin)
		if newVer == version {
			fmt.Println("warn: new binary reports the same version as the current one")
		}
	}
	if peer != "" {
		fmt.Printf("health-check peer: %s\n", peer)
	} else {
		fmt.Println("health-check peer: none — will only verify the local daemon comes back")
	}

	// Если нечем проверять связность (нет ни своего mesh-IP, ни соседа) —
	// watchdog не сможет отличить успех от провала. Предупреждаем явно.
	if selfIP == "" && peer == "" {
		fmt.Println("warn: no mesh-IP and no health-peer — watchdog can't verify connectivity, " +
			"rollback won't trigger; proceed only if you have another way back in")
	}

	// Бэкап текущего (заведомо рабочего) бинаря — парашют для отката. Нужен в
	// обоих режимах: в apk-режиме откат тоже файловый (восстановить связь
	// важнее, чем согласованность apk-учёта — её потом чинит `apk fix`).
	if err := selfupdate.CopyFile(target, upgradeBackupPath, 0o755); err != nil {
		return fmt.Errorf("backup current binary: %w", err)
	}
	fmt.Printf("backed up current binary → %s\n", upgradeBackupPath)

	// Watchdog САМ применяет апгрейд, затем проверяет связь. Применение внутри
	// watchdog (а не здесь) убирает гонку: health-проверка идёт строго ПОСЛЕ
	// того как апгрейд реально применён и демон перезапущен. В apk-режиме apk
	// качает пакет неизвестно сколько — фиксированный grace «от старта» успел бы
	// застать ещё живой СТАРЫЙ демон и ложно засчитать успех, удалив бэкап.
	//
	// Watchdog запускается из БЭКАП-бинаря (старого, заведомо рабочего) и
	// detached — переживёт разрыв SSH при пересоздании awg0, а битый новый
	// бинарь его не затронет.
	wdArgs := []string{
		"__watchdog",
		"--backup", upgradeBackupPath,
		"--target", target,
		"--self", selfIP,
		"--peer", peer,
		"--grace", grace.String(),
		"--timeout", healthTimeout.String(),
	}
	var applyDesc string
	if useApk {
		wdArgs = append(wdArgs, "--apply", "apk")
		applyDesc = "apk update && apk add --latest meshd"
	} else {
		wdArgs = append(wdArgs, "--apply", "file", "--new-binary", newBin)
		applyDesc = "replace binary + restart daemon"
	}
	if err := selfupdate.SpawnDetached(upgradeBackupPath, wdArgs, watchdogLogPath); err != nil {
		return fmt.Errorf("arm watchdog: %w", err)
	}

	fmt.Printf(`
✓ upgrade started: %s (applied by a detached watchdog)

  The mesh tunnel will blink for a few seconds. The watchdog runs the OLD
  binary, applies the upgrade, then checks connectivity for up to %s:
    • back in the mesh  → upgrade is kept, watchdog exits
    • still unreachable → previous binary is restored and restarted

  Reconnect over mesh shortly, then verify:
    meshd version
    meshd status
    cat %s   # watchdog decision log
`, applyDesc, *healthTimeout, watchdogLogPath)

	return nil
}

// cmdWatchdog — скрытая подкоманда, которую self-upgrade запускает отдельным
// detached-процессом ИЗ СТАРОГО бинаря. Сначала САМ применяет апгрейд
// (синхронно — чтобы health-проверка шла строго после), даёт grace, затем до
// timeout проверяет связность; если нода не вернулась в mesh — откатывает бинарь
// и рестартует демон. В usage не светится: служебная, вручную её не зовут.
func cmdWatchdog(args []string) error {
	fs := flag.NewFlagSet("__watchdog", flag.ExitOnError)
	backup := fs.String("backup", "", "backup binary to restore on rollback")
	target := fs.String("target", "", "binary path to restore into")
	selfIP := fs.String("self", "", "our mesh-IP (local daemon liveness probe)")
	peer := fs.String("peer", "", "neighbour mesh-IP (tunnel reachability probe)")
	grace := fs.Duration("grace", defaultHealthGrace, "settle delay before probing")
	timeout := fs.Duration("timeout", defaultHealthTimeout, "deadline before rollback")
	apply := fs.String("apply", "", "how to apply: 'apk' or 'file'")
	newBin := fs.String("new-binary", "", "new binary path (for --apply file)")
	fs.Parse(args)

	lg := log.New(os.Stdout, "watchdog: ", log.LstdFlags|log.LUTC)
	lg.Printf("armed (apply=%s self=%s peer=%s grace=%s timeout=%s)",
		*apply, *selfIP, *peer, *grace, *timeout)

	// 1) Применяем апгрейд синхронно. Если уже это провалилось — откат сразу.
	if err := applyUpgrade(lg, *apply, *target, *newBin); err != nil {
		lg.Printf("apply FAILED: %v — rolling back", err)
		rollback(lg, *target, *backup)
		return err
	}

	// 2) Даём демону подняться, затем проверяем связь до дедлайна. Проверка
	//    идёт ПОСЛЕ применения — старый демон уже не может ложно зачесть успех.
	time.Sleep(*grace)
	deadline := time.Now().Add(*timeout)
	for {
		if upgradeHealthy(*selfIP, *peer) {
			lg.Printf("mesh reachable — upgrade OK, keeping new binary")
			_ = os.Remove(*backup)
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(healthProbeInterval)
	}

	// 3) Связь не вернулась — откат к прежнему бинарю.
	lg.Printf("mesh UNREACHABLE after %s — rolling back to previous binary", *timeout)
	rollback(lg, *target, *backup)
	return nil
}

// applyUpgrade выполняет апгрейд СИНХРОННО (это важно: health-проверка в
// cmdWatchdog должна идти уже после реального применения и рестарта демона).
func applyUpgrade(lg *log.Logger, mode, target, newBin string) error {
	switch mode {
	case "apk":
		// `apk add --latest` (а не `apk upgrade`): берёт последнюю версию пакета
		// для арки устройства и переустанавливает. Это покрывает переход с ручной
		// установки (standalone .apk помечен noarch) на arch-specific пакет фида —
		// `apk upgrade` его бы проигнорировал как «другую арку». На уже-актуальной
		// версии — no-op. apk сам заменит бинарь и вызовет postupgrade → рестарт.
		//
		// АСИММЕТРИЯ с file-режимом: там бинарь preflight'ится (BinaryVersion ДО
		// подмены, пока связь цела). Здесь пакет ставится сразу — поломанный бинарь
		// из фида поймает только health-rollback (лишние grace+probe секунды
		// слепоты). Это by design: пакет ещё не скачан/не распакован, version-probe
		// делать не на чем; supply-chain закрыт подписью фида, а «фид отдал битый
		// пакет» — тот самый случай для отката.
		lg.Printf("applying: apk update && apk add --latest meshd")
		return runCmd(lg, applyTimeout, "sh", "-c", "apk update && apk add --latest meshd")
	case "file":
		lg.Printf("applying: replace %s + restart daemon", target)
		if err := selfupdate.ReplaceBinary(target, newBin, 0o755); err != nil {
			return fmt.Errorf("install new binary: %w", err)
		}
		argv := daemonRestartArgv()
		if argv == nil {
			return fmt.Errorf("no init system detected to restart daemon")
		}
		resetDaemonStartLimit(lg)
		return runCmd(lg, restartTimeout, argv[0], argv[1:]...)
	default:
		return fmt.Errorf("unknown apply mode %q", mode)
	}
}

// rollback восстанавливает прежний бинарь и рестартует демон (best-effort —
// приоритет вернуть доступ; рассинхрон apk-учёта потом чинит `apk fix`).
func rollback(lg *log.Logger, target, backup string) {
	if err := selfupdate.ReplaceBinary(target, backup, 0o755); err != nil {
		lg.Printf("rollback FAILED to restore binary: %v", err)
		return
	}
	argv := daemonRestartArgv()
	if argv == nil {
		lg.Printf("binary restored, but no init system to restart it — reboot likely needed")
		return
	}
	resetDaemonStartLimit(lg)
	if err := runCmd(lg, restartTimeout, argv[0], argv[1:]...); err != nil {
		lg.Printf("rollback restart FAILED: %v", err)
		return
	}
	lg.Printf("rolled back and restarted daemon (%s)", strings.Join(argv, " "))
}

// resetDaemonStartLimit снимает systemd start-limit ПЕРЕД рестартом. Иначе, если
// новый бинарь крэшил в цикле (Restart=on-failure, RestartSec=5s), за grace+probe-
// окно systemd упрётся в StartLimitBurst (дефолт 5/10с) и уведёт юнит в failed —
// и наш же `systemctl restart` для ОТКАТА отлетит «start request repeated too
// quickly», оставив ноду мёртвой ровно в сценарии, ради которого откат и нужен.
// На procd аналога start-limit нет — no-op. reset-failed безвреден, если юнит не
// в failed; best-effort.
func resetDaemonStartLimit(lg *log.Logger) {
	if !hasSystemdUnit() {
		return
	}
	_ = runCmd(lg, restartTimeout, "systemctl", "reset-failed", "meshd")
}

// runCmd запускает команду с таймаутом — защита от зависшего apk/systemctl,
// иначе watchdog мог бы заблокироваться навсегда, не завершив откат.
func runCmd(lg *log.Logger, timeout time.Duration, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if s := strings.TrimSpace(string(out)); s != "" {
		lg.Printf("%s: %s", name, s)
	}
	return err
}

// upgradeHealthy — вернулась ли нода в строй после рестарта. mesh-IP'шники
// превращаются в gossip-адреса (:9100); решение принимает health.Reachable.
func upgradeHealthy(selfIP, peer string) bool {
	port := strconv.Itoa(gossip.DefaultPort)
	return health.Reachable(health.Addr(selfIP, port), health.Addr(peer, port))
}
