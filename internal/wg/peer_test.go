package wg

import (
	"strings"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"

	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// TestUpdateAndRemovePeer — peer добавляется (UpdatePeer) и снимается (RemovePeer)
// на настоящем amneziawg-go device через UAPI. RemovePeer — путь revoke (снятие
// отозванной ноды на лету, без рестарта), поэтому проверяем его на реальном
// устройстве: кривой UAPI-формат отверг бы IpcSet, и revoke молча не сработал бы.
func TestUpdateAndRemovePeer(t *testing.T) {
	tdev := tuntest.NewChannelTUN()
	dev := &Device{dev: device.NewDevice(tdev.TUN(), conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelSilent, "test: "))}
	defer dev.dev.Close()

	// Минимальная конфигурация интерфейса (как при старте), чтобы UAPI принял peer.
	if err := dev.dev.IpcSet("private_key=0000000000000000000000000000000000000000000000000000000000000001\nlisten_port=0\n"); err != nil {
		t.Fatalf("ipc set base: %v", err)
	}

	priv, err := wgkey.GeneratePrivate()
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().String()
	pubHex, err := base64ToHex(pub)
	if err != nil {
		t.Fatal(err)
	}

	if err := dev.UpdatePeer(state.Peer{Label: "p", PublicKey: pub, NodeIP: "100.64.0.2"}); err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}
	if cfg, err := dev.dev.IpcGet(); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(cfg, pubHex) {
		t.Fatalf("peer not present after UpdatePeer:\n%s", cfg)
	}

	if err := dev.RemovePeer(pub); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}
	cfg, err := dev.dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(cfg, pubHex) {
		t.Fatalf("peer still present after RemovePeer:\n%s", cfg)
	}
}
