//go:build !windows

package wg

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ipLinker — unix-реализация через /sbin/ip (util-linux; есть в любом дистрибутиве
// и в OpenWrt). Netlink (vishvananda/netlink) надёжнее, но тянет зависимость —
// для одного статического бинарника exec проще и переносимее по архитектурам.
type ipLinker struct{}

func newLinker() Linker { return ipLinker{} }

// ipCmd — `ip args...` с принудительной C-локалью. Idempotency-проверки ниже
// матчат текст stdout (`File exists`, `find device`); под локализованной локалью
// (`LANG=ru` и пр.) `ip` перевёл бы сообщения и матч сломался бы. LC_ALL=C
// фиксирует английский вывод независимо от среды.
func ipCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("ip", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	return cmd
}

// AddIP назначает cidr на iface. «File exists» (адрес уже назначен) — не ошибка.
func (ipLinker) AddIP(iface, cidr string) error {
	out, err := ipCmd("addr", "add", cidr, "dev", iface).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "File exists") {
		return fmt.Errorf("ip addr add %s dev %s: %v: %s", cidr, iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (ipLinker) SetUp(iface string) error {
	if out, err := ipCmd("link", "set", "up", "dev", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set up %s: %v: %s", iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (ipLinker) SetDown(iface string) error {
	if out, err := ipCmd("link", "set", "down", "dev", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set down %s: %v: %s", iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Delete удаляет iface. Отсутствие интерфейса — не ошибка: чистим залежавшийся
// от прошлого crash'а TUN, которого может уже и не быть.
func (ipLinker) Delete(iface string) error {
	out, err := ipCmd("link", "delete", "dev", iface).CombinedOutput()
	if err != nil && !ifaceNotFound(string(out)) {
		return fmt.Errorf("ip link delete %s: %v: %s", iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ifaceNotFound — сообщил ли `ip`, что интерфейса нет. Текст разнится по
// реализации: iproute2 (Debian/util-linux) — «Cannot find device», busybox ip
// (OpenWrt) — «can't find device». Матчим общую подстроку без учёта регистра,
// чтобы Delete был идемпотентен на ОБЕИХ платформах (БАГ #1 v0.4.0: busybox-текст
// не матчился → ложный WARN «cleanup stale interface failed» при каждом старте на
// роутере). Реальные ошибки (permission и пр.) подстроку не содержат — пробрасываются.
func ifaceNotFound(ipOutput string) bool {
	return strings.Contains(strings.ToLower(ipOutput), "find device")
}
