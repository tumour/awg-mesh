// Package wg — обёртка над amneziawg-go/device: создание TUN-интерфейса,
// конфигурирование через UAPI (AmneziaWG-params + peers), live add/remove
// peer'ов без рестарта.
//
// Требует CAP_NET_ADMIN или root — для tun.CreateTUN и `ip addr add`.
package wg

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// Device — managed AmneziaWG interface (TUN + userspace device).
type Device struct {
	dev        *device.Device
	tun        tun.Device
	name       string
	listenPort int
	logger     *device.Logger
}

// New создаёт TUN-интерфейс name и инициализирует AmneziaWG-device на нём.
// device пока не сконфигурирован и не запущен — для этого Configure + Up.
func New(name string, listenPort int, logLevel int) (*Device, error) {
	tunDev, err := tun.CreateTUN(name, device.DefaultMTU)
	if err != nil {
		return nil, fmt.Errorf("create tun %s: %w", name, err)
	}
	realName, _ := tunDev.Name()
	logger := device.NewLogger(logLevel, fmt.Sprintf("(%s) ", realName))
	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), logger)
	return &Device{
		dev:        dev,
		tun:        tunDev,
		name:       realName,
		listenPort: listenPort,
		logger:     logger,
	}, nil
}

// Name — фактическое имя TUN-интерфейса (может отличаться от requested
// например если на хосте уже есть awg0, ядро выделит awg1).
func (d *Device) Name() string { return d.name }

// Configure применяет начальный конфиг (private_key + awg-params + все peer'ы).
// Использует replace_peers=true чтобы сбросить любое существующее состояние.
//
// selfPubKey — наш собственный pubkey, исключается из peer-list'а.
func (d *Device) Configure(
	priv wgkey.Private,
	awgp awgparams.Params,
	peers []state.Peer,
	selfPubKey wgkey.Public,
) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "private_key=%s\n", priv.Hex())
	fmt.Fprintf(&sb, "listen_port=%d\n", d.listenPort)
	writeAwgParams(&sb, awgp)
	sb.WriteString("replace_peers=true\n")

	for _, p := range peers {
		if p.PublicKey == selfPubKey.String() {
			continue
		}
		if err := writePeer(&sb, p); err != nil {
			return fmt.Errorf("peer %s: %w", p.Label, err)
		}
	}
	if err := d.dev.IpcSet(sb.String()); err != nil {
		return fmt.Errorf("IpcSet: %w", err)
	}
	return nil
}

// UpdatePeer добавляет или обновляет одного peer'а (incremental, без replace_peers).
// Используется при gossip-update или когда новый peer заджойнился через bootstrap.
func (d *Device) UpdatePeer(p state.Peer) error {
	var sb strings.Builder
	if err := writePeer(&sb, p); err != nil {
		return err
	}
	if err := d.dev.IpcSet(sb.String()); err != nil {
		return fmt.Errorf("IpcSet (update peer %s): %w", p.Label, err)
	}
	return nil
}

// RemovePeer удаляет peer'а из device по pubkey.
func (d *Device) RemovePeer(pubkeyBase64 string) error {
	pubHex, err := base64ToHex(pubkeyBase64)
	if err != nil {
		return err
	}
	cmd := fmt.Sprintf("public_key=%s\nremove=true\n", pubHex)
	if err := d.dev.IpcSet(cmd); err != nil {
		return fmt.Errorf("IpcSet (remove peer): %w", err)
	}
	return nil
}

// Up — поднимает device (handshake'и начинаются, UDP-сокет слушает).
func (d *Device) Up() error {
	if err := d.dev.Up(); err != nil {
		return fmt.Errorf("device.Up: %w", err)
	}
	return nil
}

// AssignIP назначает IP-адрес на TUN-интерфейс и поднимает link.
// Использует /sbin/ip из util-linux (есть в любом Linux-дистрибутиве).
// Для production желателен netlink через github.com/vishvananda/netlink,
// но для MVP exec.Command надёжнее всего.
func (d *Device) AssignIP(cidr string) error {
	if out, err := exec.Command("ip", "addr", "add", cidr, "dev", d.name).CombinedOutput(); err != nil {
		// Если адрес уже назначен (RTNETLINK answers: File exists) — не падаем.
		if !strings.Contains(string(out), "File exists") {
			return fmt.Errorf("ip addr add %s dev %s: %v: %s", cidr, d.name, err, out)
		}
	}
	if out, err := exec.Command("ip", "link", "set", "up", "dev", d.name).CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set up %s: %v: %s", d.name, err, out)
	}
	return nil
}

// Close — gracefully останавливает device и удаляет TUN-интерфейс.
func (d *Device) Close() {
	if d.dev != nil {
		d.dev.Close()
	}
}

// writeAwgParams сериализует AmneziaWG-параметры в UAPI-формат.
func writeAwgParams(sb *strings.Builder, p awgparams.Params) {
	fmt.Fprintf(sb, "jc=%d\n", p.Jc)
	fmt.Fprintf(sb, "jmin=%d\n", p.Jmin)
	fmt.Fprintf(sb, "jmax=%d\n", p.Jmax)
	fmt.Fprintf(sb, "s1=%d\n", p.S1)
	fmt.Fprintf(sb, "s2=%d\n", p.S2)
	fmt.Fprintf(sb, "h1=%d\n", p.H1)
	fmt.Fprintf(sb, "h2=%d\n", p.H2)
	fmt.Fprintf(sb, "h3=%d\n", p.H3)
	fmt.Fprintf(sb, "h4=%d\n", p.H4)
}

// writePeer добавляет один peer block в UAPI-команду.
// AllowedIPs — только NodeIP/32 (peer-to-peer routing внутри mesh).
// Endpoint и keepalive — только если у peer'а есть public-endpoint
// (иначе он сам initiates handshake, мы лишь listener).
func writePeer(sb *strings.Builder, p state.Peer) error {
	pubHex, err := base64ToHex(p.PublicKey)
	if err != nil {
		return fmt.Errorf("decode pubkey: %w", err)
	}
	fmt.Fprintf(sb, "public_key=%s\n", pubHex)
	fmt.Fprintf(sb, "allowed_ip=%s/32\n", p.NodeIP)
	if p.Endpoint != "" {
		fmt.Fprintf(sb, "endpoint=%s\n", p.Endpoint)
		fmt.Fprintf(sb, "persistent_keepalive_interval=25\n")
	}
	return nil
}

// base64ToHex — конверсия base64-pubkey (формат state.json) в hex (формат UAPI).
func base64ToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("base64: %w", err)
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("key must be 32 bytes, got %d", len(raw))
	}
	return fmt.Sprintf("%x", raw), nil
}
