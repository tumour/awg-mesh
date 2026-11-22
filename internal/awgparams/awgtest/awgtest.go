// Package awgtest — фикстуры сетевых AWG-params для тестов других пакетов (по образцу
// net/http/httptest). Один источник «какой набор валиден, какой нет», чтобы тесты
// gossip/node/mesh/wg не держали свои копии и не закрепляли наборы, которые настоящий
// device отвергнет (нулевые H-диапазоны пересекаются — такой набор не применится).
package awgtest

import "github.com/tumour/awg-mesh/internal/awgparams"

// Valid — набор, проходящий И политику (awgparams.Validate), И лимиты device
// (awgparams.CheckDeviceLimits). s4 — чтобы тест мог различать «старый» и «новый» набор.
func Valid(s4 int) awgparams.Params {
	return awgparams.Params{
		Jc: 5, Jmin: 40, Jmax: 120,
		S1: 30, S2: 40, S3: 20, S4: s4, // 148+30 != 92+40
		H1: awgparams.HeaderRange{Min: 10, Max: 20},
		H2: awgparams.HeaderRange{Min: 30, Max: 40},
		H3: awgparams.HeaderRange{Min: 50, Max: 60},
		H4: awgparams.HeaderRange{Min: 70, Max: 80},
	}
}

// Legacy — набор как у mesh, созданного на AWG-1.0 и пережившего миграцию v1→v2:
// одиночные H по всему uint32 (в т.ч. выше 2^31-1). Вне политики Validate, но device его
// принимает — нода с таким state обязана стартовать и принимать flag-day.
func Legacy() awgparams.Params {
	return awgparams.Params{
		Jc: 3, Jmin: 50, Jmax: 1000, S1: 60, S2: 4, S3: 0, S4: 0,
		H1: awgparams.HeaderRange{Min: 3_908_512_031, Max: 3_908_512_031},
		H2: awgparams.HeaderRange{Min: 2_147_483_648, Max: 2_147_483_648},
		H3: awgparams.HeaderRange{Min: 77_123_456, Max: 77_123_456},
		H4: awgparams.HeaderRange{Min: 4_294_967_295, Max: 4_294_967_295},
	}
}

// DeviceInvalid — наборы, которые device ОТВЕРГНЕТ на IpcSet, по одному на каждое правило
// библиотеки. Построены из base порчей одного поля.
func DeviceInvalid(base awgparams.Params) map[string]awgparams.Params {
	mut := func(f func(p *awgparams.Params)) awgparams.Params {
		p := base
		f(&p)
		return p
	}
	return map[string]awgparams.Params{
		"jmin>jmax":   mut(func(p *awgparams.Params) { p.Jmin = p.Jmax + 1 }),
		"S4>uint16":   mut(func(p *awgparams.Params) { p.S4 = 65536 }),
		"S1<0":        mut(func(p *awgparams.Params) { p.S1 = -1 }),
		"Jc<0":        mut(func(p *awgparams.Params) { p.Jc = -1 }),
		"H-inverted":  mut(func(p *awgparams.Params) { p.H3 = awgparams.HeaderRange{Min: p.H3.Max, Max: p.H3.Min - 1} }),
		"H-overlap":   mut(func(p *awgparams.Params) { p.H2 = p.H1 }),
		"zero-value":  {},
		"only-S4-set": {S4: 16},
		"H-all-zero": mut(func(p *awgparams.Params) {
			p.H1, p.H2, p.H3, p.H4 = awgparams.HeaderRange{}, awgparams.HeaderRange{}, awgparams.HeaderRange{}, awgparams.HeaderRange{}
		}),
		"H-touch-edges": mut(func(p *awgparams.Params) { p.H2 = awgparams.HeaderRange{Min: p.H1.Max, Max: p.H1.Max + 5} }),
	}
}

// OutsidePolicyDeviceValid — наборы вне политики Validate, которые device ПРИМЕТ.
// Отказ от них где-либо, кроме ввода оператора, = быть строже device (нода откажется от
// набора, который применил весь mesh, или не стартует после апгрейда).
func OutsidePolicyDeviceValid(base awgparams.Params) map[string]awgparams.Params {
	mut := func(f func(p *awgparams.Params)) awgparams.Params {
		p := base
		f(&p)
		return p
	}
	return map[string]awgparams.Params{
		"legacy-AWG1":   Legacy(),
		"J-zero":        mut(func(p *awgparams.Params) { p.Jc, p.Jmin, p.Jmax = 0, 0, 0 }),
		"S1/S2-collide": mut(func(p *awgparams.Params) { p.S2 = p.S1 + 56 }),
		"H-below-5":     mut(func(p *awgparams.Params) { p.H1 = awgparams.HeaderRange{Min: 1, Max: 2} }),
	}
}
