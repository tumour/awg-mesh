package main

import (
	"bytes"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/state"
)

func TestAllocateNextIPSequential(t *testing.T) {
	s := &state.State{
		NetworkCIDR: "100.64.0.0/24",
		Peers:       []state.Peer{{NodeIP: "100.64.0.1"}}, // seed
	}

	ip, err := allocateNextIP(s)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	if ip != "100.64.0.2" {
		t.Fatalf("first regular peer must get .2, got %s", ip)
	}

	// .2 и .4 заняты — аллокатор берёт первую дырку
	s.Peers = append(s.Peers,
		state.Peer{NodeIP: "100.64.0.2"},
		state.Peer{NodeIP: "100.64.0.4"},
	)
	ip, err = allocateNextIP(s)
	if err != nil {
		t.Fatalf("alloc: %v", err)
	}
	if ip != "100.64.0.3" {
		t.Fatalf("want first free .3, got %s", ip)
	}
}

func TestAllocateNextIPExhaustionSkips255(t *testing.T) {
	s := &state.State{NetworkCIDR: "100.64.0.0/24"}
	for i := 2; i <= 254; i++ {
		s.Peers = append(s.Peers, state.Peer{NodeIP: fmt.Sprintf("100.64.0.%d", i)})
	}
	// Всё до .254 занято, .255 (broadcast) не выдаётся → исчерпание
	if ip, err := allocateNextIP(s); err == nil {
		t.Fatalf("want exhaustion error, got ip %s", ip)
	}
}

func TestAllocateNextIPBadCIDR(t *testing.T) {
	s := &state.State{NetworkCIDR: "not-a-cidr"}
	if _, err := allocateNextIP(s); err == nil {
		t.Fatal("want parse error, got nil")
	}
}

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
