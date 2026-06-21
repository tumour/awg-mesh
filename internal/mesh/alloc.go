package mesh

import (
	"encoding/binary"
	"fmt"
	"net/netip"

	"github.com/tumour/awg-mesh/internal/state"
)

// FirstUsableIP — первый usable host-IP в CIDR (network + 1). Для seed на init.
// Работает для любого IPv4-префикса, не только /24.
func FirstUsableIP(cidr string) (string, error) {
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		return "", err
	}
	if !prefix.Addr().Is4() {
		return "", fmt.Errorf("IPv4-only CIDR supported, got %s", cidr)
	}
	first := prefix.Masked().Addr().Next() // network + 1
	if !prefix.Contains(first) || first == lastAddr(prefix) {
		return "", fmt.Errorf("CIDR %s too small for a host", cidr)
	}
	return first.String(), nil
}

// AllocateNextIP — следующий свободный IP в s.NetworkCIDR. Используется seed'ом
// при регистрации новой ноды. Пропускает network-адрес и broadcast (последний
// адрес префикса), .1 обычно занят seed'ом — поэтому старт с network+2.
// Broadcast вычисляется из префикса, корректно для любой маски (не только /24).
func AllocateNextIP(s *state.State) (string, error) {
	prefix, err := netip.ParsePrefix(s.NetworkCIDR)
	if err != nil {
		return "", fmt.Errorf("parse cidr: %w", err)
	}
	prefix = prefix.Masked()
	if !prefix.Addr().Is4() {
		return "", fmt.Errorf("IPv4-only CIDR supported, got %s", s.NetworkCIDR)
	}

	used := make(map[netip.Addr]bool)
	for _, p := range s.Peers {
		if ip, err := netip.ParseAddr(p.NodeIP); err == nil {
			used[ip] = true
		}
	}

	bcast := lastAddr(prefix)
	addr := prefix.Addr().Next().Next() // network+2 (.1 — seed)
	for prefix.Contains(addr) {
		if addr == bcast {
			break // broadcast не выдаём
		}
		if !used[addr] {
			return addr.String(), nil
		}
		addr = addr.Next()
	}
	return "", fmt.Errorf("no free IPs in %s", s.NetworkCIDR)
}

// lastAddr — последний адрес префикса (broadcast для IPv4): network | hostmask.
func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	base := binary.BigEndian.Uint32(a[:])
	hostMask := uint32(0xFFFFFFFF) >> uint(p.Bits())
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], base|hostMask)
	return netip.AddrFrom4(b)
}
