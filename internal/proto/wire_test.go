package proto

import (
	"bytes"
	"net"
	"testing"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	_ = c2.SetDeadline(time.Now().Add(2 * time.Second))

	payload := bytes.Repeat([]byte{0xAB}, 1500)
	errCh := make(chan error, 1)
	go func() { errCh <- WriteFrame(c1, payload) }()

	got, err := ReadFrame(c2, 2048)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: want %d bytes, got %d", len(payload), len(got))
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
}

// length-prefix и body приходят отдельными TCP-порциями — io.ReadFull обязан
// дочитать (регрессионный тест на partial read).
func TestReadFrameFragmented(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	_ = c2.SetDeadline(time.Now().Add(2 * time.Second))

	go func() {
		for _, chunk := range [][]byte{{0x00}, {0x03}, []byte("ab"), []byte("c")} {
			if _, err := c1.Write(chunk); err != nil {
				return
			}
		}
	}()

	got, err := ReadFrame(c2, 16)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if string(got) != "abc" {
		t.Fatalf("want abc, got %q", got)
	}
}

func TestReadFrameRejectsBadSizes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix []byte
	}{
		{"oversize", []byte{0x08, 0x00}}, // 2048 > max 1024
		{"zero", []byte{0x00, 0x00}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c1, c2 := net.Pipe()
			defer c1.Close()
			defer c2.Close()
			_ = c2.SetDeadline(time.Now().Add(2 * time.Second))

			go c1.Write(tc.prefix)
			if _, err := ReadFrame(c2, 1024); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestWriteFrameTooLarge(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	// > MaxMessageSize не влезает в 2-байтовый length-prefix — отказ до записи.
	if err := WriteFrame(c1, make([]byte, MaxMessageSize+1)); err == nil {
		t.Fatal("want error, got nil")
	}
}
