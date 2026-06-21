package main

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// Тесты аллокации IP переехали в internal/mesh (alloc_test.go) вместе с логикой.

func TestFramedRoundTrip(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	_ = c2.SetDeadline(time.Now().Add(2 * time.Second))

	payload := bytes.Repeat([]byte{0xAB}, 1500)
	errCh := make(chan error, 1)
	go func() { errCh <- writeFramed(c1, payload) }()

	got, err := readFramed(c2, 2048)
	if err != nil {
		t.Fatalf("readFramed: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: want %d bytes, got %d", len(payload), len(got))
	}
	if err := <-errCh; err != nil {
		t.Fatalf("writeFramed: %v", err)
	}
}

// length-prefix и body приходят отдельными TCP-порциями — io.ReadFull обязан
// дочитать (регрессионный тест на partial read).
func TestReadFramedFragmented(t *testing.T) {
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

	got, err := readFramed(c2, 16)
	if err != nil {
		t.Fatalf("readFramed: %v", err)
	}
	if string(got) != "abc" {
		t.Fatalf("want abc, got %q", got)
	}
}

func TestReadFramedRejectsBadSizes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix []byte
	}{
		{"oversize", []byte{0x08, 0x00}}, // 2048 > maxSize 1024
		{"zero", []byte{0x00, 0x00}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c1, c2 := net.Pipe()
			defer c1.Close()
			defer c2.Close()
			_ = c2.SetDeadline(time.Now().Add(2 * time.Second))

			go c1.Write(tc.prefix)
			if _, err := readFramed(c2, 1024); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestWriteFramedTooLarge(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	// > 0xFFFF не влезает в 2-байтовый length-prefix — отказ до записи
	if err := writeFramed(c1, make([]byte, 0x10000)); err == nil {
		t.Fatal("want error, got nil")
	}
}
