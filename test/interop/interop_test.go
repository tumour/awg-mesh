// Package interop — wire-совместимость текущей amneziawg-go (v3, на ней собран meshd) с
// предыдущей линейкой 0.2.x. Нужен на время раскатки: ноды обновляются по одной, и
// mesh какое-то время смешанный — старая и новая библиотека обязаны держать туннель
// друг с другом, включая flag-day и I1. Отдельный модуль, чтобы старая библиотека не
// попадала в граф зависимостей meshd.
//
// Обе библиотеки живут в одном тест-бинаре (разные module path), между ними — настоящий
// UDP на loopback, TUN in-memory (root не нужен).
//
// Гонять БЕЗ -race: в 0.2.x IpcSet пишет S/H/J неатомарно, пока receive-рутина их
// читает, — race-детектор срабатывает внутри старой библиотеки на первом же Configure.
// Это известный дефект 0.2.x (одна из причин перехода на v3, где поля атомарные);
// те же сценарии на v3 под -race проверяет internal/wg. Здесь проверяется только wire.
package interop

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

	oldconn "github.com/amnezia-vpn/amneziawg-go/conn"
	olddevice "github.com/amnezia-vpn/amneziawg-go/device"
	oldtuntest "github.com/amnezia-vpn/amneziawg-go/tun/tuntest"
	newconn "github.com/amnezia-vpn/amneziawg-go/v3/conn"
	newdevice "github.com/amnezia-vpn/amneziawg-go/v3/device"
	newtuntest "github.com/amnezia-vpn/amneziawg-go/v3/tun/tuntest"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/awgparams/awgtest"
	"github.com/tumour/awg-mesh/internal/state"
	"github.com/tumour/awg-mesh/internal/wg"
	"github.com/tumour/awg-mesh/internal/wgkey"
)

// antiReplayWindow — пауза между переинициациями handshake'а с одним пиром: больше
// гранулярности метки TAI64N (2^24 нс), иначе получатель отбросит initiation как повтор.
const antiReplayWindow = 50 * time.Millisecond

// upWithin — сколько ждём, что трафик ДОЛЖЕН пройти. Отрицательные проверки — не
// таймаутом, а счётчиком реально полученных датаграмм (notDelivered).
const upWithin = 10 * time.Second

// dev — то общее, что нужно тесту от device любой версии.
type dev interface {
	IpcSet(string) error
	IpcGet() (string, error)
	IsUnderLoad() bool
	Close()
}

type node struct {
	name     string
	lib      string // "old" | "new"
	ip       netip.Addr
	priv     wgkey.Private
	d        dev
	outbound chan<- []byte
	rx       chan []byte
	wire     *atomic.Int64 // UDP-датаграммы, реально полученные сокетом
	// cookieShape — как выглядит cookie-reply при текущих params (S3, H3); nil — не ищем.
	cookieShape atomic.Pointer[awgparams.Params]
	cookies     atomic.Int64 // сколько cookie-reply реально пришло на сокет
}

// inspect считает принятые датаграммы, похожие на cookie-reply: S3 байт паддинга +
// 64 байта сообщения, тип (LE uint32 сразу после паддинга) — в диапазоне H3.
func (n *node) inspect(pkt []byte) {
	p := n.cookieShape.Load()
	if p == nil || len(pkt) != p.S3+64 {
		return
	}
	if typ := binary.LittleEndian.Uint32(pkt[p.S3:]); typ >= p.H3.Min && typ <= p.H3.Max {
		n.cookies.Add(1)
	}
}

func newNode(t *testing.T, lib, name, ip string) *node {
	t.Helper()
	priv, err := wgkey.GeneratePrivate()
	if err != nil {
		t.Fatal(err)
	}
	n := &node{name: name, lib: lib, ip: netip.MustParseAddr(ip), priv: priv, rx: make(chan []byte, 4096), wire: new(atomic.Int64)}
	var inbound <-chan []byte
	switch lib {
	case "old":
		ct := oldtuntest.NewChannelTUN()
		n.d = olddevice.NewDevice(ct.TUN(), oldCountingBind{Bind: oldconn.NewDefaultBind(), rx: n.wire, inspect: n.inspect}, olddevice.NewLogger(olddevice.LogLevelSilent, ""))
		n.outbound, inbound = ct.Outbound, ct.Inbound
	case "new":
		ct := newtuntest.NewChannelTUN()
		n.d = newdevice.NewDevice(ct.TUN(), newCountingBind{Bind: newconn.NewDefaultBind(), rx: n.wire, inspect: n.inspect}, newdevice.NewLogger(newdevice.LogLevelSilent, ""))
		n.outbound, inbound = ct.Outbound, ct.Inbound
	default:
		t.Fatalf("lib %q", lib)
	}
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case pkt, ok := <-inbound:
				if !ok {
					return
				}
				select {
				case n.rx <- pkt:
				default:
				}
			case <-stop:
				return
			}
		}
	}()
	t.Cleanup(func() { n.d.Close(); close(stop) })
	return n
}

// peerUAPI — peer-блок ПРОДУКТОВЫМ сериализатором meshd (internal/wg), не копией:
// interop обязан проверять ровно тот формат, который meshd шлёт в device.
func peerUAPI(t *testing.T, peer *node, endpoint string) string {
	t.Helper()
	s, err := wg.PeerUAPI(state.Peer{Label: peer.name, PublicKey: peer.priv.Public().String(), NodeIP: peer.ip.String(), Endpoint: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (n *node) configure(t *testing.T, p awgparams.Params, lo awgparams.LocalObf, peers string) {
	t.Helper()
	cfg := fmt.Sprintf("private_key=%s\nlisten_port=0\n", n.priv.Hex()) + wg.NetParamsUAPI(p) + wg.LocalObfUAPI(lo) +
		"replace_peers=true\n" + peers
	if err := n.d.IpcSet(cfg); err != nil {
		t.Fatalf("%s(%s): configure: %v", n.name, n.lib, err)
	}
}

func (n *node) port(t *testing.T) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s, err := n.d.IpcGet()
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(s, "\n") {
			if v, ok := strings.CutPrefix(line, "listen_port="); ok {
				if p, _ := strconv.Atoi(v); p != 0 {
					return p
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s: device не забиндил порт", n.name)
	return 0
}

// pair: nat — за NAT (initiator-only), pub — с endpoint'ом. Библиотеки задаются явно.
func pair(t *testing.T, natLib, pubLib string, p awgparams.Params, natObf, pubObf awgparams.LocalObf) (nat, pub *node) {
	t.Helper()
	nat = newNode(t, natLib, "nat", "100.64.0.10")
	pub = newNode(t, pubLib, "pub", "100.64.0.11")
	pub.configure(t, p, pubObf, peerUAPI(t, nat, ""))
	nat.configure(t, p, natObf, peerUAPI(t, pub, fmt.Sprintf("127.0.0.1:%d", pub.port(t))))
	return nat, pub
}

func reachable(t *testing.T, from, to *node, within time.Duration) bool {
	t.Helper()
	token := randHex(t)
	deadline := time.Now().Add(within)
	for i := 0; time.Now().Before(deadline); i++ {
		select {
		case from.outbound <- udpPacket(from.ip, to.ip, fmt.Sprintf("%s#%d", token, i)):
		case <-time.After(2 * time.Second):
			t.Fatalf("%s(%s): TUN не принимает пакеты", from.name, from.lib)
		}
		if waitToken(to, token, 150*time.Millisecond) {
			return true
		}
	}
	return false
}

// Счётчики датаграмм для обеих библиотек: у них разные типы conn.Bind/ReceiveFunc.
type oldCountingBind struct {
	oldconn.Bind
	rx      *atomic.Int64
	inspect func([]byte) // каждая принятая датаграмма (для поиска cookie-reply)
}

func (b oldCountingBind) Open(port uint16) ([]oldconn.ReceiveFunc, uint16, error) {
	fns, actual, err := b.Bind.Open(port)
	out := make([]oldconn.ReceiveFunc, len(fns))
	for i, fn := range fns {
		out[i] = func(p [][]byte, sz []int, eps []oldconn.Endpoint) (int, error) {
			n, err := fn(p, sz, eps)
			b.rx.Add(int64(n))
			for k := 0; k < n; k++ {
				b.inspect(p[k][:sz[k]])
			}
			return n, err
		}
	}
	return out, actual, err
}

type newCountingBind struct {
	newconn.Bind
	rx      *atomic.Int64
	inspect func([]byte) // каждая принятая датаграмма (для поиска cookie-reply)
}

func (b newCountingBind) Open(port uint16) ([]newconn.ReceiveFunc, uint16, error) {
	fns, actual, err := b.Bind.Open(port)
	out := make([]newconn.ReceiveFunc, len(fns))
	for i, fn := range fns {
		out[i] = func(p [][]byte, sz []int, eps []newconn.Endpoint) (int, error) {
			n, err := fn(p, sz, eps)
			b.rx.Add(int64(n))
			for k := 0; k < n; k++ {
				b.inspect(p[k][:sz[k]])
			}
			return n, err
		}
	}
	return out, actual, err
}

// notDelivered — трафик from→to НЕ проходит: до сокета получателя дошло не меньше
// minDropped датаграмм, и ни одна не попала в его TUN. Датаграммы не доходят вовсе →
// падение: отказ device не доказан.
func notDelivered(t *testing.T, from, to *node) {
	t.Helper()
	const minDropped = 6
	token := randHex(t)
	start := to.wire.Load()
	deadline := time.Now().Add(upWithin)
	for i := 0; to.wire.Load()-start < minDropped; i++ {
		if time.Now().After(deadline) {
			t.Fatalf("%s→%s: до сокета дошло %d датаграмм — отказ device не доказан", from.lib, to.lib, to.wire.Load()-start)
		}
		select {
		case from.outbound <- udpPacket(from.ip, to.ip, fmt.Sprintf("%s#%d", token, i)):
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: TUN не принимает пакеты", from.lib)
		}
		if waitToken(to, token, 100*time.Millisecond) {
			t.Fatalf("%s→%s: пакет доставлен, хотя должен быть отброшен", from.lib, to.lib)
		}
	}
	if waitToken(to, token, 200*time.Millisecond) {
		t.Fatalf("%s→%s: пакет доставлен, хотя должен быть отброшен", from.lib, to.lib)
	}
}

func waitToken(n *node, token string, d time.Duration) bool {
	timeout := time.After(d)
	for {
		select {
		case pkt := <-n.rx:
			if len(pkt) > 28 && strings.HasPrefix(string(pkt[28:]), token) {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

func randHex(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// Горячая смена I1 во время handshake'ей — на обеих версиях. Под -race на 0.2.x это
// ОБЯЗАНО падать (неатомарная запись I-пакетов против чтения в SendHandshakeInitiation):
// так доказано, что сценарий ловит гонку. На v3 — чисто. В CI модуль гоняется без -race
// (гонка старой библиотеки срабатывает уже на Configure), здесь проверяется wire;
// v3-вариант под -race гоняет internal/wg (TestTunnel_I1HotSwapDuringHandshakes).
func TestInterop_I1HotSwapDuringHandshakes(t *testing.T) {
	quic, err := awgparams.GenerateQUICInitialObf("example.org", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	specs := []awgparams.LocalObf{awgparams.DefaultLocalObf(), {I1: quic}, {I1: "<b 0xc0ffee><r 40>"}}
	for _, lib := range []string{"old", "new"} {
		t.Run(lib, func(t *testing.T) {
			nat, pub := pair(t, lib, "new", generate(t), specs[0], awgparams.LocalObf{})
			if !reachable(t, nat, pub, upWithin) {
				t.Fatal("туннель не поднялся")
			}
			stop, done := make(chan struct{}), make(chan struct{})
			var swaps atomic.Int64
			go func() {
				defer close(done)
				for i := 0; ; i++ {
					select {
					case <-stop:
						return
					default:
					}
					if err := nat.d.IpcSet(wg.LocalObfUAPI(specs[i%len(specs)])); err != nil {
						t.Errorf("i1 swap: %v", err)
						return
					}
					swaps.Add(1)
				}
			}()
			// Без keepalive: иначе immediate keepalive запускает initiation ВНУТРИ IpcSet —
			// под тем же мьютексом, что и смена I1, и гонке негде случиться. Initiation
			// должна идти из data-path (пакет из TUN), как в эксплуатации.
			peerCfg := withoutKeepalive(peerUAPI(t, pub, fmt.Sprintf("127.0.0.1:%d", pub.port(t))))
			for i := 0; i < 10; i++ {
				// Anti-replay WireGuard: метка TAI64N в initiation округляется до 2^24 нс
				// (≈16.8 мс), получатель берёт только строго бо́льшую. Переинициация в том
				// же окне отбрасывается как повтор → ретрай через REKEY_TIMEOUT (5 с).
				time.Sleep(antiReplayWindow)
				before := swaps.Load()
				if err := nat.d.IpcSet(fmt.Sprintf("public_key=%s\nremove=true\n", pub.priv.Public().Hex())); err != nil {
					t.Fatal(err)
				}
				if err := nat.d.IpcSet(peerCfg); err != nil {
					t.Fatal(err)
				}
				if !reachable(t, nat, pub, upWithin) {
					t.Fatalf("handshake #%d не прошёл во время смены I1", i)
				}
				if swaps.Load() == before {
					t.Fatalf("handshake #%d без единой смены I1 — гонка не проверена", i)
				}
			}
			close(stop)
			<-done
		})
	}
}

// withoutKeepalive убирает keepalive из продуктового peer-блока (см. hot-swap-тесты).
func withoutKeepalive(block string) string {
	return strings.Replace(block, "persistent_keepalive_interval=25\n", "", 1)
}

func udpPacket(src, dst netip.Addr, payload string) []byte {
	total := 28 + len(payload)
	b := make([]byte, total)
	b[0], b[8], b[9] = 0x45, 64, 17
	binary.BigEndian.PutUint16(b[2:], uint16(total))
	s4, d4 := src.As4(), dst.As4()
	copy(b[12:], s4[:])
	copy(b[16:], d4[:])
	var sum uint32
	for i := 0; i < 20; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	for sum > 0xffff {
		sum = sum>>16 + sum&0xffff
	}
	binary.BigEndian.PutUint16(b[10:], ^uint16(sum))
	binary.BigEndian.PutUint16(b[20:], 40000)
	binary.BigEndian.PutUint16(b[22:], 40001)
	binary.BigEndian.PutUint16(b[24:], uint16(8+len(payload)))
	copy(b[28:], payload)
	return b
}

func generate(t *testing.T) awgparams.Params {
	t.Helper()
	p, err := awgparams.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Все четыре сочетания ролей и версий: старая/новая библиотека за NAT и на публичной
// стороне. Каждое — на нескольких свежих наборах params, туда и обратно.
func TestInterop_AllRoleVersionCombos(t *testing.T) {
	combos := [][2]string{{"old", "new"}, {"new", "old"}, {"new", "new"}, {"old", "old"}}
	for _, c := range combos {
		t.Run(c[0]+"-nat/"+c[1]+"-pub", func(t *testing.T) {
			for i := 0; i < 3; i++ {
				nat, pub := pair(t, c[0], c[1], generate(t), awgparams.DefaultLocalObf(), awgparams.LocalObf{})
				if !reachable(t, nat, pub, upWithin) {
					t.Fatalf("набор %d: nat(%s)→pub(%s) не доходит", i, c[0], c[1])
				}
				if !reachable(t, pub, nat, upWithin) {
					t.Fatalf("набор %d: pub(%s)→nat(%s) не доходит", i, c[1], c[0])
				}
			}
		})
	}
}

// I1-варианты, которые meshd реально раздаёт, на инициаторе любой версии.
func TestInterop_I1Variants(t *testing.T) {
	quic, err := awgparams.GenerateQUICInitialObf("example.com", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string]awgparams.LocalObf{
		"default": awgparams.DefaultLocalObf(),
		"quic":    {I1: quic},
		"dns-raw": {I1: "<b 0x1234818000010001000000000000>"},
		"i1-i5":   {I1: quic, I2: "<r 32>", I3: "<b 0xdead>", I4: "<rc 16>", I5: "<rd 8>"},
	}
	for name, lo := range variants {
		for _, c := range [][2]string{{"old", "new"}, {"new", "old"}} {
			t.Run(name+"/"+c[0]+"-nat", func(t *testing.T) {
				nat, pub := pair(t, c[0], c[1], generate(t), lo, awgparams.LocalObf{})
				if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
					t.Fatal("связность смешанной пары сломана I1-вариантом")
				}
			})
		}
	}
}

// Flag-day в смешанном mesh: обе версии перещёлкивают набор на живом туннеле (как это
// делает meshd — частичный UAPI только с сетевыми params). Одна сторона сменила → связь
// рвётся, вторая догнала → связь вернулась. Порядок «кто первый» — в обе стороны.
func TestInterop_FlagDayFlipMixed(t *testing.T) {
	for _, first := range []string{"old", "new"} {
		t.Run(first+"-flips-first", func(t *testing.T) {
			p1 := generate(t)
			p2 := generate(t)
			if p2.S4 == p1.S4 {
				p2.S4 = p1.S4%20 + 30
			}
			nat, pub := pair(t, "old", "new", p1, awgparams.DefaultLocalObf(), awgparams.LocalObf{})
			if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
				t.Fatal("до flip'а туннель не поднялся")
			}
			a, b := nat, pub
			if first == "new" {
				a, b = pub, nat
			}
			if err := a.d.IpcSet(wg.NetParamsUAPI(p2)); err != nil {
				t.Fatalf("%s: flip: %v", a.lib, err)
			}
			notDelivered(t, nat, pub) // flip одной стороны → датаграммы доходят и отбрасываются
			if err := b.d.IpcSet(wg.NetParamsUAPI(p2)); err != nil {
				t.Fatalf("%s: flip: %v", b.lib, err)
			}
			if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
				t.Fatal("обе версии на новом наборе, но связь не вернулась")
			}
		})
	}
}

// Легаси-state (H из AWG-1.0 — одиночные uint32, в т.ч. в верхней половине, после
// миграции v1→v2) обязан держать туннель между версиями: такие mesh'и существуют и
// будут обновляться на новую библиотеку.
func TestInterop_LegacyHeadersMixed(t *testing.T) {
	legacy := awgtest.Legacy()
	for _, c := range [][2]string{{"old", "new"}, {"new", "old"}} {
		t.Run(c[0]+"-nat", func(t *testing.T) {
			nat, pub := pair(t, c[0], c[1], legacy, awgparams.DefaultLocalObf(), awgparams.LocalObf{})
			if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
				t.Fatal("легаси-набор не держит туннель между версиями")
			}
		})
	}
}

// Политика Validate обязана быть строже ОБЕИХ библиотек: набор, разосланный set-params
// в смешанный mesh, должен применяться и на старых нодах. (Для новой это же свойство
// проверяет fuzz в internal/wg.)
func FuzzValidateImpliesOldLibAccepts(f *testing.F) {
	f.Add(5, 40, 120, 30, 40, 20, 16, uint32(10), uint32(20), uint32(30), uint32(40), uint32(50), uint32(60), uint32(70), uint32(80))
	f.Add(1, 1, 1, 65535, 0, 65535, 65535, uint32(5), uint32(5), uint32(6), uint32(6), uint32(7), uint32(7), uint32(1<<31-1), uint32(1<<31-1))
	f.Add(0, 0, 0, 0, 0, 0, 0, uint32(5), uint32(5), uint32(6), uint32(6), uint32(7), uint32(7), uint32(8), uint32(8))
	f.Fuzz(func(t *testing.T, jc, jmin, jmax, s1, s2, s3, s4 int, h1a, h1b, h2a, h2b, h3a, h3b, h4a, h4b uint32) {
		p := awgparams.Params{Jc: jc, Jmin: jmin, Jmax: jmax, S1: s1, S2: s2, S3: s3, S4: s4,
			H1: awgparams.HeaderRange{Min: h1a, Max: h1b}, H2: awgparams.HeaderRange{Min: h2a, Max: h2b},
			H3: awgparams.HeaderRange{Min: h3a, Max: h3b}, H4: awgparams.HeaderRange{Min: h4a, Max: h4b}}
		if awgparams.Validate(p) != nil {
			return
		}
		ct := oldtuntest.NewChannelTUN()
		d := olddevice.NewDevice(ct.TUN(), oldconn.NewDefaultBind(), olddevice.NewLogger(olddevice.LogLevelSilent, ""))
		defer d.Close()
		if err := d.IpcSet(wg.NetParamsUAPI(p)); err != nil {
			t.Fatalf("Validate пропустил набор, который старая библиотека отвергает: %v (%+v)", err, p)
		}
	})
}
