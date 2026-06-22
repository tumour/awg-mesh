package proto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"

	"github.com/flynn/noise"
)

// MaxMessageSize — лимит размера wire-кадра. Защита от OOM при кривом peer'е и
// одновременно потолок 2-байтового length-prefix (0xFFFF). 64KB достаточно для
// bootstrap-сообщений (peer-list даже на 100 нод ~10KB).
const MaxMessageSize = 65535

// WriteFrame пишет payload в w с 2-байтовым length-prefix (big-endian).
// Базовый примитив wire-формата: используется и для незашифрованных
// Noise-handshake-кадров (msg1/msg2 в bootstrap), и для шифрованных сообщений
// (WriteMessage). Отказывает, если payload не влезает в uint16.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > MaxMessageSize {
		return fmt.Errorf("frame too large: %d > %d", len(payload), MaxMessageSize)
	}
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(payload)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// ReadFrame читает 2-байтовый length-prefix + body из r, с лимитом max байт.
// io.ReadFull (не Read): на fragmented TCP частичное чтение префикса дало бы
// length из мусора и обрыв — ReadFull дочитывает. Реджектит size==0 и size>max.
func ReadFrame(r io.Reader, max int) ([]byte, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}
	size := int(binary.BigEndian.Uint16(lenBuf[:]))
	if size == 0 || size > max {
		return nil, fmt.Errorf("invalid frame size: %d (max %d)", size, max)
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return buf, nil
}

// WriteMessage сериализует v в JSON, шифрует через cs и пишет length-prefixed
// кадр (WriteFrame).
func WriteMessage(w io.Writer, cs *noise.CipherState, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	ciphertext, err := cs.Encrypt(nil, nil, raw)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	return WriteFrame(w, ciphertext)
}

// ReadMessage читает length-prefixed кадр (ReadFrame), расшифровывает через cs
// и парсит JSON в v.
func ReadMessage(r io.Reader, cs *noise.CipherState, v any) error {
	ciphertext, err := ReadFrame(r, MaxMessageSize)
	if err != nil {
		return err
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
