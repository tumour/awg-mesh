// Package wgkey — генерация и сериализация WireGuard-ключей (Curve25519).
//
// Совместимо со стандартом WireGuard: 32-байтовый приватный ключ с clamping'ом
// по RFC 7748, публичный ключ = scalar mult basepoint. Сериализация — base64
// (как в wg-quick конфигах и `awg show`).
package wgkey

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// Private — 32-байтовый WG-приватный ключ.
type Private [32]byte

// Public — 32-байтовый WG-публичный ключ.
type Public [32]byte

// GeneratePrivate возвращает новый случайный приватник с правильным Curve25519
// clamping'ом (биты по RFC 7748: clear low 3 bits of first byte, set high bit
// of last byte, clear top bit of last byte).
func GeneratePrivate() (Private, error) {
	var p Private
	if _, err := rand.Read(p[:]); err != nil {
		return Private{}, fmt.Errorf("rand.Read: %w", err)
	}
	// Curve25519 clamping
	p[0] &= 248
	p[31] &= 127
	p[31] |= 64
	return p, nil
}

// Public вычисляет публичный ключ для данного приватника.
func (p Private) Public() Public {
	var pub Public
	curve25519.ScalarBaseMult((*[32]byte)(&pub), (*[32]byte)(&p))
	return pub
}

// String кодирует ключ в base64 (как wg-quick / awg show).
func (p Private) String() string { return base64.StdEncoding.EncodeToString(p[:]) }
func (p Public) String() string  { return base64.StdEncoding.EncodeToString(p[:]) }

// ParsePrivate декодирует base64-приватник.
func ParsePrivate(s string) (Private, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return Private{}, fmt.Errorf("base64: %w", err)
	}
	if len(raw) != 32 {
		return Private{}, fmt.Errorf("private key must be 32 bytes, got %d", len(raw))
	}
	var p Private
	copy(p[:], raw)
	return p, nil
}

// ParsePublic декодирует base64-публичник.
func ParsePublic(s string) (Public, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return Public{}, fmt.Errorf("base64: %w", err)
	}
	if len(raw) != 32 {
		return Public{}, fmt.Errorf("public key must be 32 bytes, got %d", len(raw))
	}
	var pub Public
	copy(pub[:], raw)
	return pub, nil
}
