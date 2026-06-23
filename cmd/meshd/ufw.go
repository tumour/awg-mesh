package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/tumour/awg-mesh/internal/gossip"
)

// UFW-политика meshd: firewall сам НЕ трогаем. Единственное исключение —
// явный opt-in через `meshd init/join --ufw gossip|all`.
//
// Почему совсем игнорировать ufw нельзя: gossip-сервер слушает <mesh-ip>:9100,
// входящие запросы приходят через awg0. UFW с default-deny их дропает → нода
// не отдаёт peer-list, mesh вокруг неё слепнет. Остальному mesh-трафику ufw
// не мешает: ping разрешён из коробки (before.rules), исходящие туннели и
// ответный трафик пропускает conntrack. Поэтому без --ufw печатаем hint
// (printUFWHintIfNeeded) — решение остаётся за пользователем.

// meshIfaceName — имя TUN-интерфейса в ufw-правилах. Совпадает с дефолтом
// флага -iface у `meshd run`; кастомный iface — настраивай firewall руками.
const meshIfaceName = "awg0"

// validateUFWMode — проверка значения флага --ufw до начала работы команды.
func validateUFWMode(mode string) error {
	switch mode {
	case "", "gossip", "all":
		return nil
	}
	return fmt.Errorf("invalid --ufw value %q: want 'gossip' or 'all'", mode)
}

// ufwStatus возвращает (активен ли ufw, вывод `ufw status`). Любая ошибка
// (нет ufw, нет прав) трактуется как «не активен» — это best-effort проверка.
func ufwStatus() (bool, string) {
	if _, err := exec.LookPath("ufw"); err != nil {
		return false, ""
	}
	out, err := exec.Command("ufw", "status").Output()
	if err != nil {
		return false, ""
	}
	s := string(out)
	return strings.Contains(s, "Status: active"), s
}

// ufwMeshRuleExists — есть ли уже правило, пропускающее gossip с awg0.
// В `ufw status` правило 'allow in on awg0' выглядит как "Anywhere on awg0",
// а 'allow in on awg0 to any port 9100 proto tcp' — как "9100/tcp on awg0".
// Имя интерфейса матчим с trailing-границей (" awg0 "), иначе awg0 ложно
// сматчился бы как префикс awg00/awg0x (в ufw-таблице после iface всегда колонка).
func ufwMeshRuleExists(rules string) bool {
	on := " on " + meshIfaceName + " "
	return strings.Contains(rules, "Anywhere"+on) ||
		strings.Contains(rules, fmt.Sprintf("%d/tcp%s", gossip.DefaultPort, on))
}

// applyUFWMode — выполняет opt-in настройку по флагу --ufw. Пустой mode —
// no-op с hint'ом, если ufw активен и gossip закрыт. Ошибки не фатальны
// для init/join (mesh уже сконфигурирован) — печатаем warn и живём дальше.
func applyUFWMode(mode string) {
	if mode == "" {
		printUFWHintIfNeeded()
		return
	}

	active, rules := ufwStatus()
	if !active {
		fmt.Println("warn: --ufw given, but ufw is not active (or not installed) — nothing to do")
		return
	}
	if mode == "all" && ufwMeshRuleExists(rules) && strings.Contains(rules, "Anywhere on "+meshIfaceName) {
		return // уже есть широкое правило
	}

	var args []string
	switch mode {
	case "gossip":
		args = []string{"allow", "in", "on", meshIfaceName,
			"to", "any", "port", fmt.Sprint(gossip.DefaultPort), "proto", "tcp",
			"comment", "awg-mesh gossip"}
	case "all":
		args = []string{"allow", "in", "on", meshIfaceName,
			"comment", "awg-mesh internal (trust-by-tunneling)"}
	}
	if out, err := exec.Command("ufw", args...).CombinedOutput(); err != nil {
		fmt.Printf("warn: ufw %s: %v: %s\n", strings.Join(args, " "), err,
			strings.TrimSpace(string(out)))
		return
	}
	fmt.Printf("✓ ufw: %s\n", strings.Join(args, " "))
}

// printUFWHintIfNeeded — дефолтный путь (без --ufw): ufw активен, gossip
// с mesh-интерфейса закрыт → объясняем последствия и даём команды на выбор.
func printUFWHintIfNeeded() {
	active, rules := ufwStatus()
	if !active || ufwMeshRuleExists(rules) {
		return
	}
	fmt.Printf(`
WARN: UFW is active and will drop incoming gossip on %[1]s — other nodes
won't be able to pull the peer-list from this node. Allow it with ONE of:

  ufw allow in on %[1]s to any port %[2]d proto tcp   # gossip only (minimum)
  ufw allow in on %[1]s                             # full mesh access to this node

...or re-run with --ufw gossip|all. meshd never touches the firewall on its own.
`, meshIfaceName, gossip.DefaultPort)
}
