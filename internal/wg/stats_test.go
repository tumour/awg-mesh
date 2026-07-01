package wg

import (
	"bytes"
	"encoding/base64"
	"testing"
)

// parsePeerStats — чистый парсер UAPI get=1: без TUN/device. Проверяем конверсию
// hex→base64 pubkey (матчинг со state.Peer), выставление/обнуление handshake и rx/tx.

func TestParsePeerStats(t *testing.T) {
	// два пира: у первого был handshake, у второго sec=0 (никогда).
	b64A := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 32))
	b64B := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x02}, 32))
	hexA, err := base64ToHex(b64A)
	if err != nil {
		t.Fatalf("base64ToHex A: %v", err)
	}
	hexB, err := base64ToHex(b64B)
	if err != nil {
		t.Fatalf("base64ToHex B: %v", err)
	}

	uapi := "private_key=00\n" +
		"listen_port=51820\n" +
		"public_key=" + hexA + "\n" +
		"endpoint=203.0.113.10:51820\n" +
		"last_handshake_time_sec=1700000000\n" +
		"last_handshake_time_nsec=500\n" +
		"tx_bytes=1234\n" +
		"rx_bytes=5678\n" +
		"persistent_keepalive_interval=25\n" +
		"allowed_ip=100.64.0.2/32\n" +
		"public_key=" + hexB + "\n" +
		"last_handshake_time_sec=0\n" +
		"last_handshake_time_nsec=0\n" +
		"tx_bytes=0\n" +
		"rx_bytes=0\n" +
		"allowed_ip=100.64.0.3/32\n" +
		"errno=0\n"

	stats, err := parsePeerStats(uapi)
	if err != nil {
		t.Fatalf("parsePeerStats: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("stats = %d пиров, want 2", len(stats))
	}

	a := stats[0]
	if a.PublicKey != b64A {
		t.Errorf("pubkey[0] = %q, want %q (hex→base64)", a.PublicKey, b64A)
	}
	if a.LastHandshake.IsZero() {
		t.Error("handshake[0] нулевой, want выставленный")
	}
	if a.LastHandshake.Unix() != 1700000000 {
		t.Errorf("handshake[0].Unix = %d, want 1700000000", a.LastHandshake.Unix())
	}
	if a.RxBytes != 5678 || a.TxBytes != 1234 {
		t.Errorf("rx/tx[0] = %d/%d, want 5678/1234", a.RxBytes, a.TxBytes)
	}

	b := stats[1]
	if b.PublicKey != b64B {
		t.Errorf("pubkey[1] = %q, want %q", b.PublicKey, b64B)
	}
	if !b.LastHandshake.IsZero() {
		t.Errorf("handshake[1] = %v, want zero (sec=0 → никогда)", b.LastHandshake)
	}
}

// Пустой/битый ввод не должен паниковать и не наплодит пиров.
func TestParsePeerStatsEmpty(t *testing.T) {
	stats, err := parsePeerStats("errno=0\n")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(stats) != 0 {
		t.Errorf("stats = %d, want 0", len(stats))
	}
}

// Битый hex в public_key → ошибка, а не мусорный пир.
func TestParsePeerStatsBadPubkey(t *testing.T) {
	if _, err := parsePeerStats("public_key=zzzz\n"); err == nil {
		t.Error("ожидали ошибку на битом hex-pubkey, got nil")
	}
}
