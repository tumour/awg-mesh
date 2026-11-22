package wg

import (
	"bytes"
	"crypto/rand"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tumour/awg-mesh/internal/awgparams"
)

// upWithin — сколько ждём, что туннель ДОЛЖЕН пропустить трафик. Отрицательные проверки
// таймаутом не делаются: см. notDelivered (счётчик датаграмм) и состояние UAPI.
const upWithin = 10 * time.Second

// Базовый контракт data plane: NAT-нода инициирует, трафик идёт в обе стороны, на
// реальных сгенерированных params (S1-S4 + H-диапазоны) и боевом дефолтном I1.
// Прогоняем несколько свежих наборов: Generate случаен, редкий плохой набор не должен
// проскочить, «повезло один раз» — не доказательство.
func TestTunnel_NATPairBidirectional(t *testing.T) {
	for i := 0; i < 4; i++ {
		p := mustGenerate(t)
		nat, pub := natPair(t, p, awgparams.DefaultLocalObf(), awgparams.LocalObf{})
		if !reachable(t, nat, pub, upWithin) {
			t.Fatalf("набор %d: nat→pub не доходит (params %+v)", i, p)
		}
		if !reachable(t, pub, nat, upWithin) {
			t.Fatalf("набор %d: pub→nat не доходит после handshake от nat (params %+v)", i, p)
		}
	}
}

// Семантика ролей (README/CLAUDE.md): к ноде без endpoint'а инициировать нельзя.
// Пока NAT-нода не знает endpoint публичной (а значит, не может постучаться сама),
// публичная до неё не достаёт. Если это «вдруг заработает» — endpoint протёк туда,
// где его быть не должно.
//
// Вторая половина — живучесть mesh: как только NAT-нода узнала endpoint публичной
// (UpdatePeer из gossip), persistent-keepalive обязан САМ поднять туннель — публичная
// достаёт NAT-ноду, хотя та не отправила ни одного пакета данных. Без этого NAT-нода
// без своего трафика оставалась бы недостижимой.
func TestTunnel_PublicReachesNATOnlyAfterNATLearnsEndpoint(t *testing.T) {
	p := mustGenerate(t)
	nat := newTNode(t, "nat", "100.64.0.10")
	pub := newTNode(t, "pub", "100.64.0.11")
	pub.configureUp(t, p, awgparams.LocalObf{}, nat.asPeer(t, false))
	nat.configureUp(t, p, awgparams.LocalObf{}, pub.asPeer(t, false)) // endpoint публичной ещё не известен

	// Доказательство состоянием, а не таймаутом: у pub нет адреса, куда слать nat'у, а
	// nat не знает, куда стучаться к pub. Трафик pub→nat физически некуда отправить.
	if ep, ok := peerEndpoint(t, pub, nat); ok {
		t.Fatalf("у pub появился endpoint nat'а (%s), хотя nat не инициировал", ep)
	}
	if ep, ok := peerEndpoint(t, nat, pub); ok {
		t.Fatalf("у nat есть endpoint pub (%s) до UpdatePeer", ep)
	}
	nat.send(pub, "probe")
	pub.send(nat, "probe")
	if ep, ok := peerEndpoint(t, pub, nat); ok {
		t.Fatalf("после попытки отправки у pub появился endpoint nat'а (%s)", ep)
	}

	if err := nat.dev.UpdatePeer(pub.asPeer(t, true)); err != nil {
		t.Fatalf("UpdatePeer: %v", err)
	}
	if !reachable(t, pub, nat, upWithin) {
		t.Fatal("nat узнал endpoint pub, но keepalive не поднял туннель: pub→nat не доходит без трафика от nat")
	}
	if !reachable(t, nat, pub, upWithin) {
		t.Fatal("nat→pub не доходит")
	}
}

// I1 — initiator-local: любой валидный I1 на любой стороне не должен ломать связность.
// Покрываем все формы, которые meshd реально раздаёт: дефолт, QUIC-Initial с SNI,
// сырой «DNS-ответ»-литерал оператора, и разные I1 на двух сторонах.
func TestTunnel_I1VariantsKeepConnectivity(t *testing.T) {
	quic, err := awgparams.GenerateQUICInitialObf("example.com", rand.Reader)
	if err != nil {
		t.Fatalf("GenerateQUICInitialObf: %v", err)
	}
	dnsLike := "<b 0x1234818000010001000000000000>"
	cases := map[string]struct{ nat, pub awgparams.LocalObf }{
		"default-both": {awgparams.DefaultLocalObf(), awgparams.DefaultLocalObf()},
		"quic-nat":     {awgparams.LocalObf{I1: quic}, awgparams.LocalObf{}},
		"dns-raw-nat":  {awgparams.LocalObf{I1: dnsLike}, awgparams.LocalObf{}},
		"i1..i5-nat":   {awgparams.LocalObf{I1: quic, I2: "<r 32>", I3: "<b 0xdead>", I4: "<rc 16>", I5: "<rd 8>"}, awgparams.LocalObf{}},
		"different":    {awgparams.LocalObf{I1: dnsLike}, awgparams.LocalObf{I1: quic}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			nat, pub := natPair(t, mustGenerate(t), c.nat, c.pub)
			if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
				t.Fatal("связность сломана I1-вариантом")
			}
		})
	}
}

// Обфускация обязана реально участвовать в wire-формате: расхождение ЛЮБОГО сетевого
// параметра между нодами рвёт связь. Если тест зелёный при рассинхроне — параметр не
// доезжает до device (молча игнорируется), и flag-day-логика держится на воздухе.
func TestTunnel_NetParamMismatchBreaksTraffic(t *testing.T) {
	cases := map[string]func(p *awgparams.Params){
		"S1": func(p *awgparams.Params) { p.S1++ },
		"S2": func(p *awgparams.Params) { p.S2++ },
		"S4": func(p *awgparams.Params) { p.S4++ },
		"H1": func(p *awgparams.Params) { p.H1 = shiftRange(p.H1) },
		"H2": func(p *awgparams.Params) { p.H2 = shiftRange(p.H2) },
		"H4": func(p *awgparams.Params) { p.H4 = shiftRange(p.H4) },
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			p := mustGenerate(t)
			other := p
			mut(&other)
			if err := awgparams.Validate(other); err != nil {
				t.Skipf("мутация дала невалидный набор (редкое пересечение): %v", err)
			}
			nat := newTNode(t, "nat", "100.64.0.10")
			pub := newTNode(t, "pub", "100.64.0.11")
			pub.configureUp(t, p, awgparams.LocalObf{}, nat.asPeer(t, false))
			nat.configureUp(t, other, awgparams.LocalObf{}, pub.asPeer(t, true))
			notDelivered(t, nat, pub) // датаграммы доходят до сокета и отбрасываются
		})
	}
}

// shiftRange сдвигает H-диапазон далеко вверх (не пересекаясь с исходным).
func shiftRange(h awgparams.HeaderRange) awgparams.HeaderRange {
	const shift = 1 << 20
	if h.Max+shift > 1<<31-1 {
		return awgparams.HeaderRange{Min: h.Min - shift, Max: h.Max - shift}
	}
	return awgparams.HeaderRange{Min: h.Min + shift, Max: h.Max + shift}
}

// Flag-day на живом туннеле: одна сторона сменила набор → связь рвётся (рассинхрон),
// вторая догнала → связь возвращается БЕЗ рестарта/пересоздания device. Два режима:
// только S4 (как типичный flip оператора) и полностью новый набор (S+H).
func TestTunnel_FlagDayFlipLive(t *testing.T) {
	cases := map[string]func(p awgparams.Params) awgparams.Params{
		"only-S4": func(p awgparams.Params) awgparams.Params { p.S4 = p.S4%20 + 7; return p },
		"full-new": func(p awgparams.Params) awgparams.Params {
			for {
				n, err := awgparams.Generate()
				if err == nil && n.S4 != p.S4 && n.H1 != p.H1 {
					return n
				}
			}
		},
	}
	for name, next := range cases {
		t.Run(name, func(t *testing.T) {
			p1 := mustGenerate(t)
			p2 := next(p1)
			if p2.S4 == p1.S4 {
				p2.S4++
			}
			nat, pub := natPair(t, p1, awgparams.DefaultLocalObf(), awgparams.LocalObf{})
			if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
				t.Fatal("до flip'а туннель не поднялся")
			}

			if err := nat.dev.ApplyParams(p2); err != nil {
				t.Fatalf("ApplyParams nat: %v", err)
			}
			notDelivered(t, nat, pub) // flip только одной стороны → рассинхрон → отброс

			if err := pub.dev.ApplyParams(p2); err != nil {
				t.Fatalf("ApplyParams pub: %v", err)
			}
			if !reachable(t, nat, pub, upWithin) {
				t.Fatal("обе стороны на новом наборе, но nat→pub не восстановился")
			}
			if !reachable(t, pub, nat, upWithin) {
				t.Fatal("обе стороны на новом наборе, но pub→nat не восстановился")
			}
		})
	}
}

// Гонка «смена S4 при пакетах в очереди шифрования» (чинилась в upstream v3.1): гоняем
// непрерывный трафик в обе стороны и параллельно десятки раз перещёлкиваем набор на обеих
// нодах. Под -race любой несинхронизированный доступ к params — падение. После шторма
// обе ноды на одном наборе → туннель обязан работать (никакого залипшего состояния).
func TestTunnel_ParamFlipStormUnderLoad(t *testing.T) {
	p1 := mustGenerate(t)
	p2 := p1
	p2.S4 = p1.S4%20 + 30 // заметно другой S4 → другой offset в буфере
	nat, pub := natPair(t, p1, awgparams.DefaultLocalObf(), awgparams.LocalObf{})
	if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
		t.Fatal("туннель не поднялся")
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, dir := range [][2]*tnode{{nat, pub}, {pub, nat}} {
		wg.Add(1)
		go func(from, to *tnode) {
			defer wg.Done()
			payload := strings.Repeat("x", 1200) // крупные пакеты: смещение S4 упирается в размер буфера
			for {
				select {
				case <-stop:
					return
				case from.ctun.Outbound <- udpPacket(from.ip, to.ip, []byte(payload)):
				case <-time.After(time.Second):
					return
				}
			}
		}(dir[0], dir[1])
	}

	var flips sync.WaitGroup
	for _, n := range []*tnode{nat, pub} {
		flips.Add(1)
		go func(n *tnode) {
			defer flips.Done()
			for i := 0; i < 40; i++ {
				p := p1
				if i%2 == 0 {
					p = p2
				}
				if err := n.dev.ApplyParams(p); err != nil {
					t.Errorf("%s: ApplyParams #%d: %v", n.name, i, err)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}(n)
	}
	flips.Wait()
	close(stop)
	wg.Wait()

	for _, n := range []*tnode{nat, pub} {
		if err := n.dev.ApplyParams(p2); err != nil {
			t.Fatalf("%s: финальный ApplyParams: %v", n.name, err)
		}
	}
	if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
		t.Fatal("после шторма flip'ов туннель не восстановился")
	}
}

// Гонка «смена I1 во время отправки handshake» (чинилась в upstream v3.1; в 0.2.x —
// неатомарная запись I-пакетов, см. test/interop): свапер крутит ApplyObf ВСЁ время,
// пока основной цикл раз за разом заставляет device пройти ПОЛНЫЙ handshake (снять и
// вернуть пира → первый пакет триггерит initiation, которая читает I1-I5). Каждая
// итерация ждёт завершённого handshake'а — иначе тест мог бы «пройти», не отправив ни
// одной initiation во время смены. Под -race несинхронизированная запись — падение.
func TestTunnel_I1HotSwapDuringHandshakes(t *testing.T) {
	quic, err := awgparams.GenerateQUICInitialObf("example.org", rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	specs := []awgparams.LocalObf{
		awgparams.DefaultLocalObf(),
		{I1: quic},
		{I1: "<b 0xc0ffee><r 40>"},
		{I1: "<r 128>", I2: "<t>"},
	}
	nat, pub := natPair(t, mustGenerate(t), specs[0], awgparams.LocalObf{})
	if !reachable(t, nat, pub, upWithin) {
		t.Fatal("туннель не поднялся")
	}
	pubPeer := pub.asPeer(t, true)
	pubBlock, err := PeerUAPI(pubPeer)
	if err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var swaps atomic.Int64
	swapperDone := make(chan struct{})
	go func() {
		defer close(swapperDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if err := nat.dev.ApplyObf(specs[i%len(specs)]); err != nil {
				t.Errorf("ApplyObf #%d: %v", i, err)
				return
			}
			swaps.Add(1)
		}
	}()

	const handshakes = 20
	for i := 0; i < handshakes; i++ {
		time.Sleep(antiReplayWindow) // иначе initiation отбросят как повтор (см. константу)
		before := swaps.Load()
		if err := nat.dev.RemovePeer(pubPeer.PublicKey); err != nil {
			t.Fatalf("RemovePeer: %v", err)
		}
		// Без keepalive: immediate keepalive запустил бы initiation ВНУТРИ IpcSet — под тем
		// же мьютексом, что и ApplyObf, и гонке негде случиться. Initiation должна идти из
		// data-path (пакет из TUN, waitHandshake шлёт), как в эксплуатации.
		if err := nat.dev.dev.IpcSet(withoutKeepalive(pubBlock)); err != nil {
			t.Fatalf("re-add peer: %v", err)
		}
		if !waitHandshake(t, nat, pub, upWithin) {
			t.Fatalf("handshake #%d не завершился во время смены I1", i)
		}
		if swaps.Load() == before {
			t.Fatalf("handshake #%d прошёл без единой смены I1 — гонка не проверена", i)
		}
	}
	close(stop)
	<-swapperDone

	if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
		t.Fatal("после горячей смены I1 туннель не работает")
	}
}

// waitHandshake шлёт пакеты from→to, пока device from не покажет завершённый handshake
// с to (свежий пир после UpdatePeer начинает с нулевого LastHandshake).
func waitHandshake(t testing.TB, from, to *tnode, within time.Duration) bool {
	t.Helper()
	want := to.priv.Public().String()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		from.send(to, "kick")
		stats, err := from.dev.PeerStats()
		if err != nil {
			t.Fatalf("PeerStats: %v", err)
		}
		for _, s := range stats {
			if s.PublicKey == want && !s.LastHandshake.IsZero() {
				return true
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// Крупный пакет (близко к MTU awg0) обязан пройти целиком при большом S4: S4 добавляется
// к каждому data-пакету, и именно на крупных пакетах ломается расчёт буферов/MTU.
func TestTunnel_LargePacketWithLargeS4(t *testing.T) {
	p := mustGenerate(t)
	p.S4 = 64
	nat, pub := natPair(t, p, awgparams.DefaultLocalObf(), awgparams.LocalObf{})
	if !reachable(t, nat, pub, upWithin) {
		t.Fatal("туннель не поднялся")
	}
	payload := []byte(randToken(t) + strings.Repeat("y", TunMTU(p.S4)-28-16))
	if !nat.send(pub, string(payload)) {
		t.Fatal("TUN не принял пакет")
	}
	deadline := time.After(upWithin)
	for {
		select {
		case pkt := <-pub.rx:
			if bytes.Equal([]byte(udpPayload(pkt)), payload) {
				return
			}
		case <-deadline:
			t.Fatalf("пакет размером MTU=%d при S4=%d не дошёл целиком", TunMTU(p.S4), p.S4)
		}
	}
}
