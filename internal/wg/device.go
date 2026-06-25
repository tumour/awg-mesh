// Package wg — обёртка над amneziawg-go/device: создание TUN-интерфейса,
// конфигурирование через UAPI (AmneziaWG-params + peers), live add/remove
// peer'ов без рестарта. Сам Device кросс-платформенный (amneziawg-go умеет
// wintun); ОС-зависимая L3-настройка линка вынесена в Linker (linker_*.go).
//
// Требует CAP_NET_ADMIN или root — для tun.CreateTUN.
package wg

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// persistentKeepaliveSec — интервал keepalive (сек) для peer'ов с endpoint'ом:
// держит NAT-mapping/conntrack живым, чтобы к ноде можно было инициировать
// handshake. Для NAT-only peer'ов (без endpoint) не ставится — они initiator'ы.
const persistentKeepaliveSec = 25

// MTU-расчёт для awg0. WG-дефолт 1420 рассчитан на path-MTU 1500, но «трудные»
// пути (РФ→загранка, PPPoE) часто < 1500, а ICMP «fragmentation needed» на них
// блэкхолится → полноразмерные пакеты молча дропаются (PMTU-блэкхол), крупный
// TCP встаёт колом. AWG-2.0 усугубляет: s4-паддинг добавляется к КАЖДОМУ data-
// пакету сверху WG-оверхеда. Проверено на проде (sel2→hetzner, path-MTU ~1450):
// при MTU 1420 TCP РФ→загранка = 0.7 Мбит/с, при computed-MTU 1319 = 583 Мбит/с.
//
// Считаем автоматически из сетевого s4 — на каждой ноде, без ручной настройки.
const (
	// safePathMTU — консервативная цель path-MTU. Покрывает PPPoE-1492 и
	// асимметричные РФ→загранка-пути (~1420-1480). Мобилу-1280 не покрывает —
	// для неё MTU придётся занижать вручную (упирается в minMTU-пол).
	safePathMTU = 1400
	// wgWireOverhead — IPv4(20)+UDP(8)+WG-data-header(16)+Poly1305-tag(16).
	wgWireOverhead = 60
	// minMTU — пол: минимальный IPv6 MTU (RFC 8200), проходит на любом пути.
	minMTU = 1280
)

// TunMTU возвращает MTU интерфейса awg0 с учётом AWG-overhead (включая s4-паддинг
// каждого data-пакета): safePathMTU − wgWireOverhead − s4, зажатый в
// [minMTU, device.DefaultMTU]. Зовётся в node.Run при создании device.
func TunMTU(s4 int) int {
	mtu := safePathMTU - wgWireOverhead - s4
	if mtu < minMTU {
		mtu = minMTU
	}
	if mtu > device.DefaultMTU {
		mtu = device.DefaultMTU
	}
	return mtu
}

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
func New(name string, listenPort, mtu, logLevel int) (*Device, error) {
	tunDev, err := tun.CreateTUN(name, mtu)
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
	localObf awgparams.LocalObf,
	peers []state.Peer,
	selfPubKey wgkey.Public,
) error {
	var sb strings.Builder
	fmt.Fprintf(&sb, "private_key=%s\n", priv.Hex())
	fmt.Fprintf(&sb, "listen_port=%d\n", d.listenPort)
	writeAwgParams(&sb, awgp, localObf)
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

// Up — поднимает device (handshake'и начинаются, UDP-сокет слушает). Это
// userspace-up; назначение адреса и подъём kernel-линка — через Linker.
func (d *Device) Up() error {
	if err := d.dev.Up(); err != nil {
		return fmt.Errorf("device.Up: %w", err)
	}
	return nil
}

// Close — gracefully останавливает device и удаляет TUN-интерфейс.
func (d *Device) Close() {
	if d.dev != nil {
		d.dev.Close()
	}
}

// writeAwgParams сериализует AmneziaWG-параметры в UAPI-формат: сетевые (S/H/J)
// + per-node CPS-пакеты (I1-I5). Используется при полном Configure.
func writeAwgParams(sb *strings.Builder, p awgparams.Params, lo awgparams.LocalObf) {
	writeNetParams(sb, p)
	writeLocalObf(sb, lo)
}

// writeNetParams — СЕТЕВЫЕ params (одинаковы на всех нодах). H1-H4 как "min-max"
// (amneziawg-go newMagicHeader; min==max → фикс. значение). S3/S4 шлём всегда:
// lib трактует 0 как «padding выключен». Этим же набором делается reconfigure
// на лету при flag-day-смене (ApplyParams) — без replace_peers/private_key.
func writeNetParams(sb *strings.Builder, p awgparams.Params) {
	fmt.Fprintf(sb, "jc=%d\n", p.Jc)
	fmt.Fprintf(sb, "jmin=%d\n", p.Jmin)
	fmt.Fprintf(sb, "jmax=%d\n", p.Jmax)
	fmt.Fprintf(sb, "s1=%d\n", p.S1)
	fmt.Fprintf(sb, "s2=%d\n", p.S2)
	fmt.Fprintf(sb, "s3=%d\n", p.S3)
	fmt.Fprintf(sb, "s4=%d\n", p.S4)
	writeHeader(sb, "h1", p.H1)
	writeHeader(sb, "h2", p.H2)
	writeHeader(sb, "h3", p.H3)
	writeHeader(sb, "h4", p.H4)
}

// writeLocalObf — per-node CPS-пакеты I1-I5 (только непустые).
func writeLocalObf(sb *strings.Builder, lo awgparams.LocalObf) {
	for i, spec := range [5]string{lo.I1, lo.I2, lo.I3, lo.I4, lo.I5} {
		if spec != "" {
			fmt.Fprintf(sb, "i%d=%s\n", i+1, spec)
		}
	}
}

// ApplyParams применяет СЕТЕВЫЕ params к уже поднятому awg0 на лету (flag-day
// flip) — через UAPI, без replace_peers и пересоздания интерфейса. Туннели
// рехендшейкаются на новых S/H, но IP/маршруты/пиры сохраняются. Ошибка IpcSet
// (кривые params) → caller оставляет прежний набор.
func (d *Device) ApplyParams(p awgparams.Params) error {
	var sb strings.Builder
	writeNetParams(&sb, p)
	if err := d.dev.IpcSet(sb.String()); err != nil {
		return fmt.Errorf("IpcSet net-params: %w", err)
	}
	return nil
}

// writeHeader — magic-header в формате "key=min-max".
func writeHeader(sb *strings.Builder, key string, h awgparams.HeaderRange) {
	fmt.Fprintf(sb, "%s=%d-%d\n", key, h.Min, h.Max)
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
		fmt.Fprintf(sb, "persistent_keepalive_interval=%d\n", persistentKeepaliveSec)
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
