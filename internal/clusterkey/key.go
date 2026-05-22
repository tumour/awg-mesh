// Package clusterkey — генерация и сериализация cluster-secret'а.
//
// Cluster-secret = 32 байта random base32, единственный pre-shared материал
// между нодами. Используется для:
//   - HKDF-производного PSK для Noise_IKpsk2 при bootstrap-handshake
//     (см. internal/handshake)
//   - Identity-pinning seed'а: новая нода знает seed_pubkey из join-token'а,
//     Noise IK-pattern привязывает handshake к этому ключу
//
// Хранится на каждой ноде внутри state.json (поле cluster_secret) — общий
// chmod 600 для всего state-файла защищает и приватный wg-ключ, и cluster-secret.
// Не в git, не в S3-backup. При утечке — rotate через meshctl (TODO, v0.2).
package clusterkey

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// Key — cluster-secret (32 байта).
type Key [32]byte

// Generate возвращает новый случайный cluster-secret.
func Generate() (Key, error) {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		return Key{}, fmt.Errorf("rand.Read: %w", err)
	}
	return k, nil
}

// String кодирует ключ в base32-без-padding для удобной передачи в CLI.
// Формат: 52 символа [A-Z2-7], человекочитаемо, copy-paste-friendly.
func (k Key) String() string {
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(k[:]))
}

// Parse декодирует строку base32 обратно в Key. Принимает upper/lower case,
// игнорирует пробелы и дефисы (на случай если строку разбили для удобства).
func Parse(s string) (Key, error) {
	s = strings.ToUpper(strings.NewReplacer(" ", "", "-", "").Replace(s))
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(s)
	if err != nil {
		return Key{}, fmt.Errorf("base32 decode: %w", err)
	}
	if len(raw) != 32 {
		return Key{}, fmt.Errorf("cluster key must be 32 bytes, got %d", len(raw))
	}
	var k Key
	copy(k[:], raw)
	return k, nil
}
