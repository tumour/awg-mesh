package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/tumour/awg-mesh/internal/state"
)

// autoStartDaemon — пытается запустить meshd через init-систему сразу после
// init/join, чтобы у пользователя был один-шаг bootstrap. Поддерживаются
// systemd (Debian/Ubuntu, .deb-пакет) и procd (OpenWrt, .apk-пакет).
//
// Возвращает started. Если started=true — daemon работает, hint печатать
// не нужно. Если false — caller печатает hint про manual.
func autoStartDaemon(stateFile string) bool {
	if stateFile != state.DefaultPath {
		// Кастомный state-file — init-скрипты читают дефолтный, не подхватят.
		return false
	}
	return trySystemdStart() || tryProcdStart()
}

// trySystemdStart — запуск через systemctl. Работает только когда systemctl
// доступен в PATH и meshd.service unit-file установлен (через .deb-пакет).
func trySystemdStart() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		// Не systemd-система: openwrt с procd, alpine с openrc, docker и т.д.
		return false
	}
	// Проверяем что unit-file установлен. Без .deb-пакета его может не быть.
	if err := exec.Command("systemctl", "cat", "meshd.service").Run(); err != nil {
		return false
	}

	// Если daemon уже active — restart, чтобы подхватить свежий state.json.
	action := "start"
	if out, _ := exec.Command("systemctl", "is-active", "meshd").Output(); strings.TrimSpace(string(out)) == "active" {
		action = "restart"
	}

	fmt.Printf("starting meshd daemon (systemctl %s meshd)...\n", action)
	if out, err := exec.Command("systemctl", action, "meshd").CombinedOutput(); err != nil {
		fmt.Printf("warn: systemctl %s meshd failed: %v\n%s\n",
			action, err, strings.TrimSpace(string(out)))
		return false
	}

	// Enable чтобы перезапускался при ребуте. Тихо игнорируем ошибку
	// (могут быть уже enabled через postinst .deb-пакета).
	_ = exec.Command("systemctl", "enable", "meshd").Run()

	fmt.Println("✓ meshd daemon started")
	return true
}

// tryProcdStart — запуск через procd-init (OpenWrt). Init-скрипт
// /etc/init.d/meshd ставится .apk-пакетом; /etc/rc.common отличает
// OpenWrt от систем где /etc/init.d/meshd мог бы быть sysvinit-скриптом.
func tryProcdStart() bool {
	if _, err := os.Stat("/etc/init.d/meshd"); err != nil {
		return false
	}
	if _, err := os.Stat("/etc/rc.common"); err != nil {
		return false
	}

	// restart вместо start: если daemon уже работает (повторный join),
	// подхватываем свежий state.json; на остановленном сервисе stop — noop.
	fmt.Println("starting meshd daemon (/etc/init.d/meshd restart)...")
	if out, err := exec.Command("/etc/init.d/meshd", "restart").CombinedOutput(); err != nil {
		fmt.Printf("warn: /etc/init.d/meshd restart failed: %v\n%s\n",
			err, strings.TrimSpace(string(out)))
		return false
	}

	// Enable чтобы перезапускался при ребуте. Тихо игнорируем ошибку
	// (может быть уже enabled через post-install .apk-пакета).
	_ = exec.Command("/etc/init.d/meshd", "enable").Run()

	fmt.Println("✓ meshd daemon started")
	return true
}

// printManualStartHint — для случая когда auto-start не отработал.
func printManualStartHint() {
	fmt.Println(`
NEXT: start the daemon manually:

  systemctl enable --now meshd       # standard systemd-based hosts
  /etc/init.d/meshd enable && /etc/init.d/meshd start   # OpenWrt

Verify with:

  meshd status
  ip addr show awg0`)
}
