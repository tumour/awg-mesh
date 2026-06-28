package awgparams

import "testing"

// validParams — заведомо корректный набор (база, которую тесты ломают по одному полю).
func validParams() Params {
	return Params{
		Jc: 5, Jmin: 40, Jmax: 120,
		S1: 30, S2: 40, S3: 20, S4: 16, // 148+30=178 != 92+40=132 → ок
		H1: HeaderRange{Min: 10, Max: 20},
		H2: HeaderRange{Min: 30, Max: 40},
		H3: HeaderRange{Min: 50, Max: 60},
		H4: HeaderRange{Min: 70, Max: 80},
	}
}

// Generate ОБЯЗАН давать набор, проходящий Validate (один источник истины о валидности).
func TestValidate_GenerateIsValid(t *testing.T) {
	for i := 0; i < 64; i++ {
		p, err := Generate()
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if err := Validate(p); err != nil {
			t.Fatalf("Generate выдал невалидный набор: %v (%+v)", err, p)
		}
	}
}

func TestValidate_AcceptsValid(t *testing.T) {
	if err := Validate(validParams()); err != nil {
		t.Fatalf("валидный набор отвергнут: %v", err)
	}
}

// Главный брик-констрейнт: padded init (148+S1) == response (92+S2) → amneziawg отвергнет
// IpcSet → Configure падает на КАЖДОЙ ноде → ВСЯ сеть лежит. Должно ловиться ДО анонса.
func TestValidate_RejectsEqualPaddedSizes(t *testing.T) {
	p := validParams()
	p.S1, p.S2 = 30, 30+56 // 148+30 == 92+86
	if Validate(p) == nil {
		t.Fatal("совпадение padded init/response должно отвергаться (брик всей сети)")
	}
}

// H-диапазоны: пересечение → получатель не различит message-type'ы → сеть не сходится.
func TestValidate_RejectsOverlappingHeaders(t *testing.T) {
	p := validParams()
	p.H2 = HeaderRange{Min: 15, Max: 35} // пересекается с H1 [10,20]
	if Validate(p) == nil {
		t.Fatal("пересекающиеся H-диапазоны должны отвергаться")
	}
}

// H-диапазон ниже допустимого минимума (Min<=4) — невалиден.
func TestValidate_RejectsLowHeader(t *testing.T) {
	p := validParams()
	p.H1 = HeaderRange{Min: 3, Max: 100}
	if Validate(p) == nil {
		t.Fatal("H Min<=4 должен отвергаться")
	}
}

// Перевёрнутый диапазон (Min>Max) — невалиден.
func TestValidate_RejectsInvertedHeader(t *testing.T) {
	p := validParams()
	p.H3 = HeaderRange{Min: 200, Max: 100}
	if Validate(p) == nil {
		t.Fatal("H Min>Max должен отвергаться")
	}
}

// Отрицательные S/junk и пустой junk-диапазон (Jmax<Jmin) — невалидны.
func TestValidate_RejectsNegativeAndEmptyJunk(t *testing.T) {
	for name, mut := range map[string]func(*Params){
		"S1<0":      func(p *Params) { p.S1 = -1 },
		"S4<0":      func(p *Params) { p.S4 = -1 },
		"Jc<0":      func(p *Params) { p.Jc = -1 },
		"Jmax<Jmin": func(p *Params) { p.Jmin, p.Jmax = 80, 40 },
	} {
		t.Run(name, func(t *testing.T) {
			p := validParams()
			mut(&p)
			if Validate(p) == nil {
				t.Fatalf("%s должно отвергаться", name)
			}
		})
	}
}
