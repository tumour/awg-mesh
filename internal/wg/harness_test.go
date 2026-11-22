package wg

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/tuntest"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// Харнесс для интеграционных тестов data plane: настоящие amneziawg-go device'ы,
// обёрнутые нашим wg.Device, поверх НАСТОЯЩЕГО UDP на loopback. TUN — in-memory
// (tuntest.ChannelTUN), поэтому root/CAP_NET_ADMIN не нужны. Через этот харнесс
// проходит весь путь meshd: Configure/UpdatePeer/ApplyParams/ApplyObf → UAPI → wire.

// tnode — одна тестовая нода.
type tnode struct {
	name string
	ip   netip.Addr
	priv wgkey.Private
	dev  *Device
	ctun *tuntest.ChannelTUN
	rx   chan []byte   // пакеты, которые device отдал «в ОС» (дренаж Inbound)
	wire *atomic.Int64 // сколько UDP-датаграмм РЕАЛЬНО пришло на сокет ноды
}

// newTNode создаёт ноду с mesh-IP ip. Inbound у ChannelTUN небуферизован: без
// постоянного дренажа receive-рутина device встаёт, поэтому читаем его в фоне.
func newTNode(t testing.TB, name, ip string) *tnode {
	t.Helper()
	priv, err := wgkey.GeneratePrivate()
	if err != nil {
		t.Fatal(err)
	}
	ctun := tuntest.NewChannelTUN()
	wire := new(atomic.Int64)
	bind := countingBind{Bind: conn.NewDefaultBind(), rx: wire}
	dev := &Device{
		dev:  device.NewDevice(ctun.TUN(), bind, device.NewLogger(device.LogLevelSilent, "")),
		name: name,
	}
	n := &tnode{name: name, ip: netip.MustParseAddr(ip), priv: priv, dev: dev, ctun: ctun, rx: make(chan []byte, 4096), wire: wire}
	stop := make(chan struct{})
	go n.drain(stop)
	t.Cleanup(func() {
		dev.Close()
		close(stop)
	})
	return n
}

func (n *tnode) drain(stop <-chan struct{}) {
	for {
		select {
		case pkt, ok := <-n.ctun.Inbound:
			if !ok {
				return
			}
			select {
			case n.rx <- pkt:
			default: // переполнение — тест всё равно ищет по токену, старое не важно
			}
		case <-stop:
			return
		}
	}
}

// asPeer — peer-запись этой ноды в чужом state. withEndpoint=false — нода «за NAT»
// (initiator-only), true — нода с public-endpoint на loopback.
func (n *tnode) asPeer(t testing.TB, withEndpoint bool) state.Peer {
	t.Helper()
	p := state.Peer{Label: n.name, PublicKey: n.priv.Public().String(), NodeIP: n.ip.String()}
	if withEndpoint {
		p.Endpoint = fmt.Sprintf("127.0.0.1:%d", n.port(t))
	}
	return p
}

// configureUp — Configure + Up. Ошибка Configure — фатальна для теста.
func (n *tnode) configureUp(t testing.TB, p awgparams.Params, lo awgparams.LocalObf, peers ...state.Peer) {
	t.Helper()
	if err := n.dev.Configure(n.priv, p, lo, peers, n.priv.Public()); err != nil {
		t.Fatalf("%s: Configure: %v", n.name, err)
	}
	if err := n.dev.Up(); err != nil {
		t.Fatalf("%s: Up: %v", n.name, err)
	}
}

// uapi — текущий UAPI-дамп device (get=1).
func (n *tnode) uapi(t testing.TB) string {
	t.Helper()
	s, err := n.dev.dev.IpcGet()
	if err != nil {
		t.Fatalf("%s: IpcGet: %v", n.name, err)
	}
	return s
}

// port — фактический UDP-порт, на который забиндился device (listen_port=0 → ядро выбрало).
func (n *tnode) port(t testing.TB) int {
	t.Helper()
	v, ok := uapiValue(n.uapi(t), "listen_port")
	if !ok {
		t.Fatalf("%s: listen_port нет в UAPI", n.name)
	}
	port, err := strconv.Atoi(v)
	if err != nil || port == 0 {
		t.Fatalf("%s: кривой listen_port %q", n.name, v)
	}
	return port
}

// uapiValue — первое значение key в UAPI-дампе (device-секция идёт первой).
func uapiValue(uapi, key string) (string, bool) {
	for _, line := range strings.Split(uapi, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok && k == key {
			return v, true
		}
	}
	return "", false
}

// send кладёт IPv4/UDP-пакет from→to в TUN ноды-отправителя (как будто его отправила ОС).
// false — TUN не принял за таймаут (device встал).
func (n *tnode) send(to *tnode, payload string) bool {
	select {
	case n.ctun.Outbound <- udpPacket(n.ip, to.ip, []byte(payload)):
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

// reachable — доходит ли трафик from→to за within: шлём пакеты с уникальным токеном
// каждые ~150мс (первые уходят в handshake), ждём хотя бы один на стороне получателя.
func reachable(t testing.TB, from, to *tnode, within time.Duration) bool {
	t.Helper()
	token := randToken(t)
	deadline := time.Now().Add(within)
	for i := 0; time.Now().Before(deadline); i++ {
		if !from.send(to, fmt.Sprintf("%s#%d", token, i)) {
			t.Fatalf("%s: TUN не принимает пакеты — device завис", from.name)
		}
		if to.waitPrefix(token, 150*time.Millisecond) {
			return true
		}
	}
	return false
}

// waitPrefix ждёт пакет, payload которого начинается с prefix.
func (n *tnode) waitPrefix(prefix string, d time.Duration) bool {
	timeout := time.After(d)
	for {
		select {
		case pkt := <-n.rx:
			if strings.HasPrefix(udpPayload(pkt), prefix) {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

func randToken(t testing.TB) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// peerEndpoint — endpoint, который device ноды n держит для пира peer (из UAPI-дампа).
func peerEndpoint(t testing.TB, n, peer *tnode) (string, bool) {
	t.Helper()
	want := "public_key=" + peer.priv.Public().Hex()
	inPeer := false
	for _, line := range strings.Split(n.uapi(t), "\n") {
		if strings.HasPrefix(line, "public_key=") {
			inPeer = line == want
			continue
		}
		if v, ok := strings.CutPrefix(line, "endpoint="); ok && inPeer {
			return v, true
		}
	}
	return "", false
}

// withoutKeepalive убирает keepalive из продуктового peer-блока (см. hot-swap-тесты).
func withoutKeepalive(block string) string {
	return strings.Replace(block, fmt.Sprintf("persistent_keepalive_interval=%d\n", persistentKeepaliveSec), "", 1)
}

// countingBind считает UDP-датаграммы, реально полученные сокетом device. Нужен, чтобы
// отрицательная проверка («трафик НЕ проходит») опиралась на факт «пакеты дошли до
// сокета и были отброшены», а не на «за N мс ничего не пришло» — второе на медленном CI
// молча зеленеет, даже если отправитель вообще ничего не слал.
type countingBind struct {
	conn.Bind
	rx *atomic.Int64
}

func (b countingBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	fns, actual, err := b.Bind.Open(port)
	wrapped := make([]conn.ReceiveFunc, len(fns))
	for i, fn := range fns {
		wrapped[i] = func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
			n, err := fn(packets, sizes, eps)
			b.rx.Add(int64(n))
			return n, err
		}
	}
	return wrapped, actual, err
}

// antiReplayWindow — пауза между переинициациями handshake'а с одним пиром. Метка
// TAI64N в initiation округляется до 2^24 нс (≈16.8 мс), а получатель принимает только
// строго бо́льшую: переинициация в том же окне отбрасывается как повтор, и device
// ждёт ретрая REKEY_TIMEOUT (5 с) — тест «работает», но в сотни раз медленнее.
const antiReplayWindow = 50 * time.Millisecond

// minDroppedDatagrams — сколько датаграмм отправителя должно ДОЙТИ до сокета получателя,
// чтобы «ни один пакет не доставлен в TUN» считалось доказательством отказа, а не задержки.
const minDroppedDatagrams = 6

// notDelivered доказывает, что трафик from→to НЕ проходит: шлёт пакеты, пока сокет
// получателя не примет minDroppedDatagrams новых датаграмм, и проверяет, что ни один
// пакет с токеном не попал в TUN получателя. Если датаграммы не доходят вовсе — тест
// падает: доказать отказ device нечем (это сеть/отправитель, а не обфускация).
func notDelivered(t testing.TB, from, to *tnode) {
	t.Helper()
	token := randToken(t)
	start := to.wire.Load()
	deadline := time.Now().Add(upWithin)
	for i := 0; to.wire.Load()-start < minDroppedDatagrams; i++ {
		if time.Now().After(deadline) {
			t.Fatalf("%s→%s: до сокета дошло %d датаграмм из %d нужных — отказ device не доказан",
				from.name, to.name, to.wire.Load()-start, minDroppedDatagrams)
		}
		if !from.send(to, fmt.Sprintf("%s#%d", token, i)) {
			t.Fatalf("%s: TUN не принимает пакеты — device завис", from.name)
		}
		if to.waitPrefix(token, 100*time.Millisecond) {
			t.Fatalf("%s→%s: пакет доставлен, хотя должен быть отброшен", from.name, to.name)
		}
	}
	if to.waitPrefix(token, 200*time.Millisecond) {
		t.Fatalf("%s→%s: пакет доставлен, хотя должен быть отброшен", from.name, to.name)
	}
}

// udpPacket — минимальный корректный IPv4/UDP-пакет (UDP-checksum 0 — допустим для IPv4).
func udpPacket(src, dst netip.Addr, payload []byte) []byte {
	total := 20 + 8 + len(payload)
	b := make([]byte, total)
	b[0] = 0x45 // IPv4, IHL=5
	binary.BigEndian.PutUint16(b[2:], uint16(total))
	b[8] = 64 // TTL
	b[9] = 17 // UDP
	s4, d4 := src.As4(), dst.As4()
	copy(b[12:16], s4[:])
	copy(b[16:20], d4[:])
	binary.BigEndian.PutUint16(b[10:], ipv4Checksum(b[:20]))
	binary.BigEndian.PutUint16(b[20:], 40000)
	binary.BigEndian.PutUint16(b[22:], 40001)
	binary.BigEndian.PutUint16(b[24:], uint16(8+len(payload)))
	copy(b[28:], payload)
	return b
}

func udpPayload(pkt []byte) string {
	if len(pkt) < 28 {
		return ""
	}
	return string(pkt[28:])
}

func ipv4Checksum(h []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(h); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(h[i:]))
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

// natPair поднимает типовую связку mesh: nat — нода за NAT (без endpoint, initiator-only),
// pub — нода с public-endpoint. Сетевые params общие, I1 у каждой свой.
func natPair(t testing.TB, p awgparams.Params, natObf, pubObf awgparams.LocalObf) (nat, pub *tnode) {
	t.Helper()
	nat = newTNode(t, "nat", "100.64.0.10")
	pub = newTNode(t, "pub", "100.64.0.11")
	pub.configureUp(t, p, pubObf, nat.asPeer(t, false))
	nat.configureUp(t, p, natObf, pub.asPeer(t, true))
	return nat, pub
}

// mustGenerate — свежий валидный набор сетевых params.
func mustGenerate(t testing.TB) awgparams.Params {
	t.Helper()
	p, err := awgparams.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
