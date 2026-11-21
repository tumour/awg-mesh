package awgparams

import (
	"math"
	"testing"
)

// CheckDeviceLimits — зеркало правил, по которым amneziawg-go отвергает IpcSet. Она
// НЕ политика (политика — Validate): всё, что device принимает, она обязана принять,
// иначе нода, пережившая апгрейд со старым state, перестанет применять flag-day.
func TestCheckDeviceLimits_RejectsWhatDeviceRejects(t *testing.T) {
	for name, mut := range map[string]func(*Params){
		"S1<0":        func(p *Params) { p.S1 = -1 },
		"S4>uint16":   func(p *Params) { p.S4 = 65536 },
		"S3>uint16":   func(p *Params) { p.S3 = 1 << 20 },
		"Jc<0":        func(p *Params) { p.Jc = -1 },
		"Jmin<0":      func(p *Params) { p.Jmin = -1 },
		"Jmax<0":      func(p *Params) { p.Jmin, p.Jmax = 0, -1 },
		"jmin>jmax":   func(p *Params) { p.Jmin, p.Jmax = 9, 8 },
		"H-inverted":  func(p *Params) { p.H2 = HeaderRange{Min: 41, Max: 40} },
		"H-overlap":   func(p *Params) { p.H3 = HeaderRange{Min: 15, Max: 15} }, // внутри H1 [10,20]
		"H-same":      func(p *Params) { p.H4 = p.H1 },
		"H-touch-end": func(p *Params) { p.H2 = HeaderRange{Min: 20, Max: 25} }, // границы включительные
	} {
		t.Run(name, func(t *testing.T) {
			p := validParams()
			mut(&p)
			if CheckDeviceLimits(p) == nil {
				t.Fatalf("%s: device такое отвергнет, а CheckDeviceLimits пропустил", name)
			}
		})
	}
}

// Наборы ВНЕ политики Validate, но приемлемые для device: легаси-state (H из AWG-1.0 —
// одиночный uint32 по всему диапазону, мигрированный в {n,n}), ноль junk/padding,
// совпадение padded init/response. Отказ здесь = нода перестаёт применять flag-day.
func TestCheckDeviceLimits_AcceptsWhatDeviceAccepts(t *testing.T) {
	for name, mut := range map[string]func(*Params){
		"legacy-H-upper-half": func(p *Params) {
			p.H1, p.H2 = HeaderRange{Min: 3_000_000_000, Max: 3_000_000_000}, HeaderRange{Min: math.MaxUint32, Max: math.MaxUint32}
		},
		"H-below-5":     func(p *Params) { p.H1 = HeaderRange{Min: 1, Max: 2} },
		"J-zero":        func(p *Params) { p.Jc, p.Jmin, p.Jmax = 0, 0, 0 },
		"S-zero":        func(p *Params) { p.S1, p.S2, p.S3, p.S4 = 0, 0, 0, 0 },
		"S1/S2-collide": func(p *Params) { p.S1, p.S2 = 30, 86 },
		"S=uint16max":   func(p *Params) { p.S4 = 65535 },
	} {
		t.Run(name, func(t *testing.T) {
			p := validParams()
			mut(&p)
			if err := CheckDeviceLimits(p); err != nil {
				t.Fatalf("%s: device это примет, CheckDeviceLimits не должна быть строже: %v", name, err)
			}
		})
	}
}

// Политика строже device: всё, что прошло Validate, обязано пройти CheckDeviceLimits
// (иначе set-params анонсирует набор, который ноды не применят).
func FuzzValidateImpliesDeviceLimits(f *testing.F) {
	f.Add(5, 40, 120, 30, 40, 20, 16, uint32(10), uint32(20), uint32(30), uint32(40), uint32(50), uint32(60), uint32(70), uint32(80))
	f.Add(1, 1, 1, 65535, 0, 65535, 65535, uint32(5), uint32(5), uint32(6), uint32(6), uint32(7), uint32(7), uint32(1<<31-1), uint32(1<<31-1))
	f.Fuzz(func(t *testing.T, jc, jmin, jmax, s1, s2, s3, s4 int, h1a, h1b, h2a, h2b, h3a, h3b, h4a, h4b uint32) {
		p := Params{Jc: jc, Jmin: jmin, Jmax: jmax, S1: s1, S2: s2, S3: s3, S4: s4,
			H1: HeaderRange{h1a, h1b}, H2: HeaderRange{h2a, h2b}, H3: HeaderRange{h3a, h3b}, H4: HeaderRange{h4a, h4b}}
		if Validate(p) == nil && CheckDeviceLimits(p) != nil {
			t.Fatalf("Validate пропустил, device отвергнет: %v (%+v)", CheckDeviceLimits(p), p)
		}
	})
}
