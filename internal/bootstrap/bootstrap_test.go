package bootstrap

import (
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/proto"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

func genKey(t *testing.T) (wgkey.Private, wgkey.Public) {
	t.Helper()
	priv, err := wgkey.GeneratePrivate()
	if err != nil {
		t.Fatalf("GeneratePrivate: %v", err)
	}
	return priv, priv.Public()
}

// startSeed поднимает bootstrap-listener на loopback с готовым seed-state и
// возвращает его адрес, psk и pubkey для клиентского Join.
func startSeed(t *testing.T) (addr string, psk []byte, seedPub wgkey.Public) {
	t.Helper()
	seedPriv, seedPub := genKey(t)
	params, err := awgparams.Generate()
	if err != nil {
		t.Fatalf("awgparams: %v", err)
	}
	psk = make([]byte, 32)
	if _, err := rand.Read(psk); err != nil {
		t.Fatalf("rand: %v", err)
	}

	path := filepath.Join(t.TempDir(), "state.json")
	st := &state.State{
		NodeLabel:   "seed",
		NetworkCIDR: "100.64.0.0/24",
		PrivateKey:  seedPriv.String(),
		PublicKey:   seedPub.String(),
		NodeIP:      "100.64.0.1",
		IsSeed:      true,
		AwgParams:   params,
		Peers: []state.Peer{
			{Label: "seed", PublicKey: seedPub.String(), NodeIP: "100.64.0.1", IsSeed: true, Endpoint: "1.2.3.4:51820"},
		},
	}
	if err := st.Save(path); err != nil {
		t.Fatalf("save state: %v", err)
	}
	store := state.NewStore(path)

	// Свободный порт на loopback (pick-then-close — стандартный паттерн для теста).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr = ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	go func() { _ = Serve(ctx, addr, store, seedPriv, seedPub, psk, nil, logger) }()

	// Ждём готовности listener'а (throwaway-коннект → handleConn получит EOF).
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("seed did not start listening")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return addr, psk, seedPub
}

// Честный join: Noise-ключ совпадает с заявленным pubkey → seed выдаёт IP.
func TestBootstrapHonestJoin(t *testing.T) {
	addr, psk, seedPub := startSeed(t)

	cPriv, cPub := genKey(t)
	resp, err := Join(addr, psk, cPriv, cPub, seedPub[:], proto.HelloRequest{
		Version:   proto.ProtoVersion,
		Label:     "client",
		PublicKey: cPub.String(),
	})
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if resp.Status != "ok" {
		t.Fatalf("want ok, got %q (%s)", resp.Status, resp.Error)
	}
	if resp.YourIP == "" {
		t.Fatal("seed did not assign an IP")
	}
}

// Identity binding: Noise аутентифицирует ключ A, но HelloRequest заявляет чужой
// pubkey B → регистрация отвергнута (нельзя вписать фантомный ключ).
func TestBootstrapRejectsIdentityMismatch(t *testing.T) {
	addr, psk, seedPub := startSeed(t)

	cPriv, cPub := genKey(t) // Noise аутентифицирует ЭТОТ ключ
	_, otherPub := genKey(t) // а заявим ЧУЖОЙ pubkey

	resp, err := Join(addr, psk, cPriv, cPub, seedPub[:], proto.HelloRequest{
		Version:   proto.ProtoVersion,
		Label:     "evil",
		PublicKey: otherPub.String(), // ≠ аутентифицированный cPub
	})
	if err != nil {
		t.Fatalf("Join transport: %v", err)
	}
	if resp.Status != "error" {
		t.Fatalf("want rejection, got status %q ip=%q", resp.Status, resp.YourIP)
	}
	if resp.Error != "identity mismatch" {
		t.Fatalf("want 'identity mismatch', got %q", resp.Error)
	}
}
