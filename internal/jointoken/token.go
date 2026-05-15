// Package jointoken — сериализация всех данных, нужных новой ноде для bootstrap'а,
// в один base64-токен.
//
// Формат: base64url(JSON{secret, seed_pubkey, seed_endpoint}). Кодируется в одну
// длинную строку (~150 символов), которую seed печатает в выводе `meshd init`,
// а пользователь копирует на новую ноду одной командой.
//
// Безопасность: токен содержит cluster-secret, который при компрометации даёт
// доступ ко всему mesh'у. Передавать его off-band (scp, ssh, password manager),
// никогда не светить в чатах/логах.
package jointoken

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Token — пакет данных для bootstrap-join.
type Token struct {
	Secret       string `json:"secret"`        // cluster-secret в base32 (52 chars)
	SeedPubKey   string `json:"seed_pubkey"`   // публичный ключ seed-ноды в base64
	SeedEndpoint string `json:"seed_endpoint"` // host:port bootstrap-listener'а seed'а
}

// Encode сериализует Token в строку base64url (URL-safe, без padding).
func Encode(t Token) (string, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Decode парсит токен обратно.
func Decode(s string) (Token, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Token{}, fmt.Errorf("base64: %w", err)
	}
	var t Token
	if err := json.Unmarshal(raw, &t); err != nil {
		return Token{}, fmt.Errorf("json: %w", err)
	}
	if t.Secret == "" || t.SeedPubKey == "" || t.SeedEndpoint == "" {
		return Token{}, fmt.Errorf("token missing required fields")
	}
	return t, nil
}
