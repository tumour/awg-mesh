package wg

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// PeerStat — live-статистика одного peer'а из UAPI (get=1). PublicKey уже
// сконвертирован в base64 (формат state.Peer) для матчинга с доменом.
type PeerStat struct {
	PublicKey     string    // base64 WG-encoded
	LastHandshake time.Time // zero = handshake ещё не было
	RxBytes       uint64
	TxBytes       uint64

	hsSec  int64 // сырьё UAPI до финализации LastHandshake
	hsNsec int64
}

// PeerStats читает live-статистику всех peer'ов через UAPI get. Дешёвая
// локальная операция (форматирование состояния device в память), без сети.
func (d *Device) PeerStats() ([]PeerStat, error) {
	uapi, err := d.dev.IpcGet()
	if err != nil {
		return nil, fmt.Errorf("IpcGet: %w", err)
	}
	return parsePeerStats(uapi)
}

// parsePeerStats — чистый парсер UAPI-текста в []PeerStat. Каждый peer-блок
// начинается со строки public_key=<hex>; последующие поля до следующего
// public_key относятся к текущему пиру. Неизвестные ключи игнорируются.
func parsePeerStats(uapi string) ([]PeerStat, error) {
	var stats []PeerStat
	idx := -1 // индекс текущего пира; индекс (не указатель) устойчив к realloc'у append'а

	for _, line := range strings.Split(uapi, "\n") {
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "public_key":
			b64, err := hexToBase64(val)
			if err != nil {
				return nil, fmt.Errorf("peer pubkey: %w", err)
			}
			stats = append(stats, PeerStat{PublicKey: b64})
			idx = len(stats) - 1
		case "last_handshake_time_sec":
			if idx >= 0 {
				stats[idx].hsSec, _ = strconv.ParseInt(val, 10, 64)
			}
		case "last_handshake_time_nsec":
			if idx >= 0 {
				stats[idx].hsNsec, _ = strconv.ParseInt(val, 10, 64)
			}
		case "rx_bytes":
			if idx >= 0 {
				stats[idx].RxBytes, _ = strconv.ParseUint(val, 10, 64)
			}
		case "tx_bytes":
			if idx >= 0 {
				stats[idx].TxBytes, _ = strconv.ParseUint(val, 10, 64)
			}
		}
	}

	// Финализация: sec/nsec → LastHandshake. sec==0 && nsec==0 ⇒ handshake не было
	// (оставляем zero-time). Порядок строк sec/nsec тут не важен.
	for i := range stats {
		if stats[i].hsSec != 0 || stats[i].hsNsec != 0 {
			stats[i].LastHandshake = time.Unix(stats[i].hsSec, stats[i].hsNsec).UTC()
		}
	}
	return stats, nil
}

// hexToBase64 — конверсия hex-pubkey (формат UAPI) в base64 (формат state.json).
// Инверсия base64ToHex.
func hexToBase64(h string) (string, error) {
	raw, err := hex.DecodeString(h)
	if err != nil {
		return "", fmt.Errorf("hex: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("key must be 32 bytes, got %d", len(raw))
	}
	return base64.StdEncoding.EncodeToString(raw), nil
}
