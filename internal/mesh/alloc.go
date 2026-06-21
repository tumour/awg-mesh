package mesh

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/tumour/awg-mesh/internal/state"
)

// FirstUsableIP — первый usable host-IP в CIDR (network + 1).
// Для "10.10.0.0/24" вернёт "10.10.0.1". Используется на init для seed-ноды.
func FirstUsableIP(cidr string) (string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	ip := ipnet.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("IPv4-only CIDR supported, got %s", cidr)
	}
	next := make(net.IP, 4)
	copy(next, ip)
	next[3]++
	return next.String(), nil
}

// AllocateNextIP — следующий свободный IP в s.NetworkCIDR. Используется seed'ом
// при регистрации новой ноды (bootstrap). .1 — обычно seed, поэтому начинаем
// с .2; .255 (broadcast в /24) пропускаем.
func AllocateNextIP(s *state.State) (string, error) {
	prefix, err := netip.ParsePrefix(s.NetworkCIDR)
	if err != nil {
		return "", fmt.Errorf("parse cidr: %w", err)
	}
	used := make(map[netip.Addr]bool)
	for _, p := range s.Peers {
		ip, err := netip.ParseAddr(p.NodeIP)
		if err == nil {
			used[ip] = true
		}
	}
	addr := prefix.Addr().Next().Next() // начинаем с .2
	for prefix.Contains(addr) {
		if !used[addr] && addr.As4()[3] != 255 {
			return addr.String(), nil
		}
		addr = addr.Next()
	}
	return "", fmt.Errorf("no free IPs in %s", s.NetworkCIDR)
}
