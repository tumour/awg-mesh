package proto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/flynn/noise"
)

// MaxMessageSize — лимит размера wire-сообщения. Защита от OOM при кривом peer'е.
// 64KB достаточно для bootstrap-сообщений (peer-list даже на 100 нод ~10KB).
const MaxMessageSize = 65535

// WriteMessage сериализует v в JSON, шифрует через cs, пишет в w с 2-байтовым
// length-prefix (big-endian).
func WriteMessage(w io.Writer, cs *noise.CipherState, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	ciphertext, err := cs.Encrypt(nil, nil, raw)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	if len(ciphertext) > MaxMessageSize {
		return fmt.Errorf("ciphertext too large: %d > %d", len(ciphertext), MaxMessageSize)
	}

	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(ciphertext)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	if _, err := w.Write(ciphertext); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// ReadMessage читает length-prefixed ciphertext из r, расшифровывает через cs
// и парсит JSON в v.
func ReadMessage(r io.Reader, cs *noise.CipherState, v any) error {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return fmt.Errorf("read length: %w", err)
	}
	size := binary.BigEndian.Uint16(lenBuf[:])
	if size == 0 || size > MaxMessageSize {
		return fmt.Errorf("invalid message size: %d", size)
	}
	ciphertext := make([]byte, size)
	if _, err := io.ReadFull(r, ciphertext); err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	plaintext, err := cs.Decrypt(nil, nil, ciphertext)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}
	if err := json.Unmarshal(plaintext, v); err != nil {
		return fmt.Errorf("unmarshal: %w", err)
	}
	return nil
}
