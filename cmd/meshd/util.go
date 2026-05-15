package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// firstUsableIP — первый usable host-IP в CIDR (network + 1).
// Для "10.10.0.0/24" вернёт "10.10.0.1".
func firstUsableIP(cidr string) (string, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", err
	}
	ip := ipnet.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("IPv4-only CIDR supported, got %s", cidr)
	}
	// network + 1
	next := make(net.IP, 4)
	copy(next, ip)
	next[3]++
	return next.String(), nil
}

// parsePort вытаскивает port из строки вида ":51820" или "0.0.0.0:51820".
// Возвращает 0 если не парсится — для клиентов это означает "не слушаем".
func parsePort(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// строка типа ":51820" — net.SplitHostPort это норм обрабатывает,
		// но если только число — попробуем напрямую
		if p, err2 := strconv.Atoi(strings.TrimPrefix(addr, ":")); err2 == nil {
			return p
		}
		return 0
	}
	p, _ := strconv.Atoi(portStr)
	return p
}
