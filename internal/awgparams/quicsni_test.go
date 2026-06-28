package awgparams

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// detRandom — 32 байта TLS-random 0x00..0x1f, как в эталоне scratchpad/gen_det.mjs.
func detRandom() []byte {
	r := make([]byte, 32)
	for i := range r {
		r[i] = byte(i)
	}
	return r
}

// TestQUICInitialObfVector — байт-в-байт сверка с эталоном проверенного JS-генератора
// (mini_quic_generator, доказан 240/240 на живой трансгран. сети). Фиксированные входы
// (dcid=0x42, TLS-random=0x00..0x1f, SNI=example.com, level 0) → стабильная I1-строка.
// Малейшее расхождение в QUIC Initial / Initial-AEAD / header-protection ломает вектор.
func TestQUICInitialObfVector(t *testing.T) {
	const want = "<b 0xcc00000001014200004055f6794ffbb2f0b8cdb2f084bc644da454922cc881>" +
		"<r 23>" +
		"<b 0xbc2ec5e5fca3cbd6cf0eed46b2decb68809e755baa5b8af5d731>" +
		"<r 16>"

	got, err := quicInitialObf("example.com", []byte{0x42}, detRandom())
	if err != nil {
		t.Fatalf("quicInitialObf: %v", err)
	}
	if got != want {
		t.Fatalf("I1 vector mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestQUICInitialObfStructure — независимая (помимо вектора) проверка свойств: spec из
// чередующихся <b 0x..>/<r N>, первый литерал — валидный QUIC v1 long-header Initial с
// нашим DCID, и есть random-заполнители. SNI зашифрован внутри Initial-AEAD (DPI читает
// его, расшифровав по DCID), поэтому в открытом виде в пакете его НЕТ — его корректность
// гарантирует байт-вектор TestQUICInitialObfVector.
func TestQUICInitialObfStructure(t *testing.T) {
	got, err := quicInitialObf("example.com", []byte{0x42}, detRandom())
	if err != nil {
		t.Fatalf("quicInitialObf: %v", err)
	}
	if !strings.HasPrefix(got, "<b 0x") {
		t.Fatalf("spec must start with a literal <b 0x..> chunk, got %q", got)
	}
	if !strings.Contains(got, "<r ") {
		t.Fatalf("spec must contain <r N> random fillers (per-send entropy), got %s", got)
	}

	end := strings.IndexByte(got, '>')
	raw, err := hex.DecodeString(got[len("<b 0x"):end])
	if err != nil {
		t.Fatalf("first literal chunk is not valid hex: %v", err)
	}
	if len(raw) < 7 {
		t.Fatalf("first literal chunk too short for a QUIC header: %d bytes", len(raw))
	}
	if raw[0]&0xc0 != 0xc0 {
		t.Fatalf("first byte %#02x is not a QUIC long-header (bits 0xc0)", raw[0])
	}
	if v := raw[1:5]; !bytes.Equal(v, []byte{0x00, 0x00, 0x00, 0x01}) {
		t.Fatalf("not QUIC v1: version bytes %x", v)
	}
	if raw[5] != 0x01 || raw[6] != 0x42 {
		t.Fatalf("DCID mismatch: len=%#02x val=%#02x, want len=1 val=0x42", raw[5], raw[6])
	}
}

// TestGenerateQUICInitialObfRandomness — публичный API читает рандом из io.Reader: два
// вызова с РАЗНЫМ рандомом дают разные I1 (per-node уникальность), с одинаковым — равные.
func TestGenerateQUICInitialObfRandomness(t *testing.T) {
	seedA := bytes.Repeat([]byte{0xAA}, 64)
	seedB := bytes.Repeat([]byte{0xBB}, 64)

	a1, err := GenerateQUICInitialObf("example.com", bytes.NewReader(seedA))
	if err != nil {
		t.Fatalf("gen a1: %v", err)
	}
	a2, err := GenerateQUICInitialObf("example.com", bytes.NewReader(seedA))
	if err != nil {
		t.Fatalf("gen a2: %v", err)
	}
	b1, err := GenerateQUICInitialObf("example.com", bytes.NewReader(seedB))
	if err != nil {
		t.Fatalf("gen b1: %v", err)
	}
	if a1 != a2 {
		t.Fatal("same randomness must yield identical I1 (deterministic core)")
	}
	if a1 == b1 {
		t.Fatal("different randomness must yield different I1 (per-node uniqueness)")
	}
}
