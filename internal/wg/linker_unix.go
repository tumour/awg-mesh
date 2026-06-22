//go:build !windows

package wg

import (
	"fmt"
	"os/exec"
	"strings"
)

// ipLinker — unix-реализация через /sbin/ip (util-linux; есть в любом дистрибутиве
// и в OpenWrt). Netlink (vishvananda/netlink) надёжнее, но тянет зависимость —
// для одного статического бинарника exec проще и переносимее по архитектурам.
type ipLinker struct{}

func newLinker() Linker { return ipLinker{} }

// AddIP назначает cidr на iface. «File exists» (адрес уже назначен) — не ошибка.
func (ipLinker) AddIP(iface, cidr string) error {
	out, err := exec.Command("ip", "addr", "add", cidr, "dev", iface).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "File exists") {
		return fmt.Errorf("ip addr add %s dev %s: %v: %s", cidr, iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (ipLinker) SetUp(iface string) error {
	if out, err := exec.Command("ip", "link", "set", "up", "dev", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set up %s: %v: %s", iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (ipLinker) SetDown(iface string) error {
	if out, err := exec.Command("ip", "link", "set", "down", "dev", iface).CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set down %s: %v: %s", iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Delete удаляет iface. Отсутствие интерфейса («Cannot find device») — не ошибка:
// чистим залежавшийся от прошлого crash'а TUN, которого может уже и не быть.
func (ipLinker) Delete(iface string) error {
	out, err := exec.Command("ip", "link", "delete", "dev", iface).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "Cannot find device") {
		return fmt.Errorf("ip link delete %s: %v: %s", iface, err, strings.TrimSpace(string(out)))
	}
	return nil
}
