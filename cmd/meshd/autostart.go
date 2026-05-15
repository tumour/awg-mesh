package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/tumour/awg-mesh/internal/state"
)

// autoStartDaemon — пытается запустить meshd через systemctl сразу после
// init/join, чтобы у пользователя был один-шаг bootstrap.
//
// Работает только когда:
//   - state-file = дефолтный путь (/etc/meshd/state.json), который ждёт unit
//   - systemctl доступен в PATH
//   - meshd.service unit-file установлен (например через .deb-пакет)
//
// Возвращает (started, нужен_ли_manual_hint). Если started=true — daemon работает,
// hint печатать не нужно. Если false — caller печатает hint про manual.
func autoStartDaemon(stateFile string) bool {
	if stateFile != state.DefaultPath {
		// Кастомный state-file — systemd-юнит читает дефолтный, не подхватит.
		return false
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		// Не systemd-система: alpine с openrc, docker container и т.д.
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

// printManualStartHint — для случая когда auto-start не отработал.
func printManualStartHint() {
	fmt.Println(`
NEXT: start the daemon manually:

  systemctl enable --now meshd       # standard systemd-based hosts

Verify with:

  meshd status
  ip addr show awg0`)
}
