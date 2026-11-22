package wg

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/tuntest"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/awgparams/awgtest"
)

// netParamsFromUAPI вытаскивает сетевые params из UAPI-дампа device. Нулевые значения
// device не печатает → отсутствующий ключ = 0.
func netParamsFromUAPI(t testing.TB, uapi string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, k := range []string{"jc", "jmin", "jmax", "s1", "s2", "s3", "s4", "h1", "h2", "h3", "h4"} {
		v, ok := uapiValue(uapi, k)
		if !ok {
			v = "0"
		}
		out[k] = v
	}
	return out
}

// expectedNetParams — то, что device обязан показать после успешного применения p.
func expectedNetParams(p awgparams.Params) map[string]string {
	h := func(r awgparams.HeaderRange) string {
		if r.Min == r.Max {
			return strconv.FormatUint(uint64(r.Min), 10)
		}
		return fmt.Sprintf("%d-%d", r.Min, r.Max)
	}
	return map[string]string{
		"jc": strconv.Itoa(p.Jc), "jmin": strconv.Itoa(p.Jmin), "jmax": strconv.Itoa(p.Jmax),
		"s1": strconv.Itoa(p.S1), "s2": strconv.Itoa(p.S2), "s3": strconv.Itoa(p.S3), "s4": strconv.Itoa(p.S4),
		"h1": h(p.H1), "h2": h(p.H2), "h3": h(p.H3), "h4": h(p.H4),
	}
}

func assertDeviceParams(t testing.TB, n *tnode, want awgparams.Params, why string) {
	t.Helper()
	got := netParamsFromUAPI(t, n.uapi(t))
	exp := expectedNetParams(want)
	for k, v := range exp {
		if got[k] != v {
			t.Errorf("%s: device %s=%s, want %s", why, k, got[k], v)
		}
	}
}

// Контракт ApplyParams, на который опирается flag-day («ошибка → остаётся прежний
// набор»): отвергнутый набор НЕ должен частично примениться. amneziawg-go v3 при
// jmin>jmax успевает записать H и S ДО отказа — без собственной проверки до IpcSet
// нода осталась бы с половиной нового набора (рассинхрон со всем mesh).
func TestApplyParams_RejectedSetLeavesDeviceUntouched(t *testing.T) {
	good := mustGenerate(t)
	// Порча поверх заметно другого набора: если device успеет записать H/S до отказа,
	// assertDeviceParams это увидит.
	changed := good
	changed.H1 = shiftRange(good.H1)
	changed.S1 = good.S1 + 1
	for name, bad := range awgtest.DeviceInvalid(changed) {
		t.Run(name, func(t *testing.T) {
			n := newTNode(t, "n", "100.64.0.20")
			n.configureUp(t, good, awgparams.LocalObf{})
			if err := n.dev.ApplyParams(bad); err == nil {
				t.Fatalf("ApplyParams принял набор, который device отвергает: %+v", bad)
			}
			assertDeviceParams(t, n, good, "после отвергнутого ApplyParams")
		})
	}
}

// Обратная сторона: ApplyParams НЕ строже device. Набор вне политики Validate, но
// приемлемый для библиотеки (легаси H из AWG-1.0 в верхней половине uint32, ноль junk,
// совпадение padded-размеров) обязан применяться — его разослал seed, и нода, которая
// откажется, останется на старом наборе одна против всего mesh.
func TestApplyParams_NotStricterThanDevice(t *testing.T) {
	good := mustGenerate(t)
	for name, p := range awgtest.OutsidePolicyDeviceValid(good) {
		t.Run(name, func(t *testing.T) {
			n := newTNode(t, "n", "100.64.0.20")
			n.configureUp(t, good, awgparams.LocalObf{})
			if err := n.dev.ApplyParams(p); err != nil {
				t.Fatalf("ApplyParams строже device: %v", err)
			}
			assertDeviceParams(t, n, p, "ApplyParams")
		})
	}
}

// Старт ноды после апгрейда: state.json пережил миграцию v1→v2, H в нём — одиночные
// uint32 из AWG-1.0 (могут лежать выше 2^31-1, политика Validate такие не выдала бы).
// Библиотека их принимает → Configure обязан поднять device. Иначе апгрейд молча кладёт
// ноду без out-of-band — ровно сценарий потери связи.
func TestConfigure_LegacyStateOutsidePolicyStillStarts(t *testing.T) {
	legacy := awgtest.Legacy()
	nat := newTNode(t, "nat", "100.64.0.10")
	pub := newTNode(t, "pub", "100.64.0.11")
	pub.configureUp(t, legacy, awgparams.LocalObf{}, nat.asPeer(t, false))
	nat.configureUp(t, legacy, awgparams.DefaultLocalObf(), pub.asPeer(t, true))
	if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
		t.Fatal("легаси-набор поднялся, но трафик не идёт")
	}
}

// Свойство «Validate ⇒ device примет», на граничных значениях. Если Validate пропустит
// набор, который device отвергнет, — flag-day разошлёт его по всему mesh и Configure
// упадёт на каждой ноде на следующем старте.
func TestValidatedBoundaryParamsAcceptedByDevice(t *testing.T) {
	base := mustGenerate(t)
	cases := map[string]func(p *awgparams.Params){
		"S=uint16max": func(p *awgparams.Params) { p.S1, p.S2, p.S3, p.S4 = 65535, 65534, 65535, 65535 },
		"S=0-pad":     func(p *awgparams.Params) { p.S1, p.S2, p.S3, p.S4 = 0, 0, 0, 0 },
		"J=1":         func(p *awgparams.Params) { p.Jc, p.Jmin, p.Jmax = 1, 1, 1 },
		"J-huge":      func(p *awgparams.Params) { p.Jc, p.Jmin, p.Jmax = 1<<20, 1<<20, 1<<30 },
		"H-degenerate": func(p *awgparams.Params) {
			p.H1, p.H2, p.H3, p.H4 = awgparams.HeaderRange{Min: 5, Max: 5}, awgparams.HeaderRange{Min: 6, Max: 6},
				awgparams.HeaderRange{Min: 7, Max: 7}, awgparams.HeaderRange{Min: 8, Max: 8}
		},
		"H-top": func(p *awgparams.Params) {
			const top = 1<<31 - 1
			p.H4 = awgparams.HeaderRange{Min: top - 10, Max: top}
		},
	}
	for name, mut := range cases {
		t.Run(name, func(t *testing.T) {
			p := base
			mut(&p)
			if err := awgparams.Validate(p); err != nil {
				t.Skipf("набор не валиден по Validate (это не про device): %v", err)
			}
			n := newTNode(t, "n", "100.64.0.20")
			n.configureUp(t, p, awgparams.LocalObf{})
			assertDeviceParams(t, n, p, "Configure")
			if err := n.dev.ApplyParams(base); err != nil {
				t.Fatalf("ApplyParams base: %v", err)
			}
			if err := n.dev.ApplyParams(p); err != nil {
				t.Fatalf("device отверг набор, прошедший Validate: %v", err)
			}
			assertDeviceParams(t, n, p, "ApplyParams")
		})
	}
}

// FuzzDeviceLimitsMatchDevice — на произвольных входах: (1) CheckDeviceLimits отвергает
// РОВНО то, что отвергает СЫРОЙ IpcSet (в обе стороны — ни строже, ни мягче);
// (2) Validate ⇒ device примет; (3) принятый набор device показывает ровно как задан.
// Ловит любое расхождение наших констрейнтов с upstream при следующем апгрейде.
func FuzzDeviceLimitsMatchDevice(f *testing.F) {
	seed := func(p awgparams.Params) {
		f.Add(p.Jc, p.Jmin, p.Jmax, p.S1, p.S2, p.S3, p.S4,
			p.H1.Min, p.H1.Max, p.H2.Min, p.H2.Max, p.H3.Min, p.H3.Max, p.H4.Min, p.H4.Max)
	}
	for i := 0; i < 4; i++ {
		p, err := awgparams.Generate()
		if err != nil {
			f.Fatal(err)
		}
		seed(p)
		p.S4, p.S3 = 65535, 65536
		seed(p)
		p.Jc, p.Jmin, p.Jmax = 0, 0, 0
		seed(p)
		p.Jmin, p.Jmax = 9, 8
		seed(p)
	}
	for _, base := range []awgparams.Params{awgtest.Valid(16), awgtest.Legacy()} {
		seed(base)
		for _, p := range awgtest.DeviceInvalid(base) {
			seed(p)
		}
		for _, p := range awgtest.OutsidePolicyDeviceValid(base) {
			seed(p)
		}
	}
	big := awgtest.Legacy()
	big.Jc = 1 << 33 // за пределами uint32 (на 64-бит)
	seed(big)
	f.Fuzz(func(t *testing.T, jc, jmin, jmax, s1, s2, s3, s4 int,
		h1a, h1b, h2a, h2b, h3a, h3b, h4a, h4b uint32) {
		p := awgparams.Params{
			Jc: jc, Jmin: jmin, Jmax: jmax, S1: s1, S2: s2, S3: s3, S4: s4,
			H1: awgparams.HeaderRange{Min: h1a, Max: h1b}, H2: awgparams.HeaderRange{Min: h2a, Max: h2b},
			H3: awgparams.HeaderRange{Min: h3a, Max: h3b}, H4: awgparams.HeaderRange{Min: h4a, Max: h4b},
		}
		ctun := tuntest.NewChannelTUN()
		dev := device.NewDevice(ctun.TUN(), conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
		defer dev.Close()
		devErr := dev.IpcSet(NetParamsUAPI(p))
		limErr := awgparams.CheckDeviceLimits(p)
		if (devErr == nil) != (limErr == nil) {
			t.Fatalf("CheckDeviceLimits разошлась с device: device=%v limits=%v\n%+v", devErr, limErr, p)
		}
		if awgparams.Validate(p) == nil && devErr != nil {
			t.Fatalf("Validate пропустил набор, который device отвергает: %v\n%+v", devErr, p)
		}
		if devErr != nil {
			return
		}
		uapi, err := dev.IpcGet()
		if err != nil {
			t.Fatal(err)
		}
		got := netParamsFromUAPI(t, uapi)
		for k, v := range expectedNetParams(p) {
			if got[k] != v {
				t.Fatalf("device применил %s=%s, want %s (%+v)", k, got[k], v, p)
			}
		}
	})
}

// PeerStats на НАСТОЯЩЕМ UAPI-дампе v3: в device-секции появились новые ключи
// (random_trailers, disable_cookies, …), keepalive печатается только ненулевым. Парсер
// обязан их пережить и вернуть живой handshake и rx/tx — на этом стоит live-статус API.
func TestPeerStats_RealDeviceAfterHandshake(t *testing.T) {
	nat, pub := natPair(t, mustGenerate(t), awgparams.DefaultLocalObf(), awgparams.LocalObf{})
	if !reachable(t, nat, pub, upWithin) || !reachable(t, pub, nat, upWithin) {
		t.Fatal("туннель не поднялся")
	}
	for _, c := range []struct{ self, other *tnode }{{nat, pub}, {pub, nat}} {
		stats, err := c.self.dev.PeerStats()
		if err != nil {
			t.Fatalf("%s: PeerStats: %v", c.self.name, err)
		}
		if len(stats) != 1 {
			t.Fatalf("%s: %d пиров в stats, want 1: %+v", c.self.name, len(stats), stats)
		}
		s := stats[0]
		if s.PublicKey != c.other.priv.Public().String() {
			t.Errorf("%s: pubkey %q, want %q", c.self.name, s.PublicKey, c.other.priv.Public().String())
		}
		if s.LastHandshake.IsZero() || time.Since(s.LastHandshake) > time.Minute {
			t.Errorf("%s: LastHandshake=%v, want свежий", c.self.name, s.LastHandshake)
		}
		if s.RxBytes == 0 || s.TxBytes == 0 {
			t.Errorf("%s: rx=%d tx=%d, want оба > 0", c.self.name, s.RxBytes, s.TxBytes)
		}
	}
}

// Keepalive ставится ТОЛЬКО пиру с endpoint'ом и ровно 25с: v3 сделал интервал
// диапазоном — формат "25" обязан по-прежнему пониматься как фиксированное значение.
func TestKeepalive_OnlyForEndpointPeers(t *testing.T) {
	nat, pub := natPair(t, mustGenerate(t), awgparams.LocalObf{}, awgparams.LocalObf{})
	if v, ok := uapiValue(nat.uapi(t), "persistent_keepalive_interval"); !ok || v != strconv.Itoa(persistentKeepaliveSec) {
		t.Errorf("nat→pub (у pub есть endpoint): keepalive=%q ok=%v, want %d", v, ok, persistentKeepaliveSec)
	}
	if v, ok := uapiValue(pub.uapi(t), "persistent_keepalive_interval"); ok {
		t.Errorf("pub→nat (у nat нет endpoint'а): keepalive=%q, want не выставлен", v)
	}
}

// Фикстуры awgtest сверяются с НАСТОЯЩИМ device: всё, что названо «device отвергнет»,
// он отвергает; всё, что «примет», — принимает. Иначе тесты gossip/node/mesh на этих
// фикстурах проверяли бы выдуманную границу.
func TestAwgtestFixturesMatchDevice(t *testing.T) {
	try := func(p awgparams.Params) error {
		ctun := tuntest.NewChannelTUN()
		dev := device.NewDevice(ctun.TUN(), conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
		defer dev.Close()
		return dev.IpcSet(NetParamsUAPI(p))
	}
	if err := try(awgtest.Valid(16)); err != nil {
		t.Errorf("Valid отвергнут device: %v", err)
	}
	for _, base := range []awgparams.Params{awgtest.Valid(16), awgtest.Legacy()} {
		for name, p := range awgtest.DeviceInvalid(base) {
			if try(p) == nil {
				t.Errorf("DeviceInvalid[%s] device ПРИНЯЛ: %+v", name, p)
			}
		}
	}
	for name, p := range awgtest.OutsidePolicyDeviceValid(awgtest.Valid(16)) {
		if err := try(p); err != nil {
			t.Errorf("OutsidePolicyDeviceValid[%s] device отверг: %v", name, err)
		}
	}
}
