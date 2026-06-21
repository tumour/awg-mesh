package main

import (
	"net"
	"os"
	"strconv"
	"strings"
)

// fileExists — есть ли файл/каталог по пути (best-effort, ошибки = «нет»).
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// (firstUsableIP переехал в internal/mesh.FirstUsableIP — единый IPAM-домен.)

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
