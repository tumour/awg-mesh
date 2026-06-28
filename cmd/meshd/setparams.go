package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/mesh"
	"github.com/tumour/awg-mesh/internal/state"
)

// cmdSetParams — ЕДИНАЯ команда конфигурирования mesh, запускается ТОЛЬКО на seed
// (он authoritative по всему раздаваемому конфигу). Принимает ЯВНЫЕ аргументы и
// раздаёт их всем нодам; ноды только применяют присланное. Два класса полей:
//
//   - obf-обход (--sni): seed бампит политику, применяет свой I1 и active-push'ем
//     раздаёт per-node I1 всем. I1 не рвёт туннель → применяется сразу, без flip;
//   - сетевые params (--s1..--s4/--jc/--jmin/--jmax/--h1..--h4 или --regenerate):
//     рвут туннель → confirm-then-flip. Кладём новый набор в Pending БЕЗ ApplyAt и
//     раздаём; момент flip seed назначит сам, когда ВСЕ подтвердят приём (так flip не
//     стартует, пока кто-то не получил, и ни одна нода не теряется).
//
// Незаданные сетевые поля не меняются (override поверх ТЕКУЩИХ применённых params).
// Новый набор проходит awgparams.Validate ДО анонса — невалидный отвергается, чтобы
// flag-day не положил Configure на всей сети.
func cmdSetParams(args []string) error {
	fs := flag.NewFlagSet("set-params", flag.ExitOnError)
	stateFlag := fs.String("state-file", state.DefaultPath, "path to state file")
	sni := fs.String("sni", "", "обход DPI: домен для per-node I1 (QUIC-мимик); seed раздаёт всем нодам")
	regenerate := fs.Bool("regenerate", false, "сгенерировать свежий случайный набор сетевых params (ротация всего)")
	s1 := fs.Int("s1", 0, "S1: padding handshake-init")
	s2 := fs.Int("s2", 0, "S2: padding handshake-response")
	s3 := fs.Int("s3", 0, "S3: padding cookie-reply")
	s4 := fs.Int("s4", 0, "S4: padding КАЖДОГО transport-пакета (ключ против flow-анализа)")
	jc := fs.Int("jc", 0, "Jc: число junk-пакетов перед handshake")
	jmin := fs.Int("jmin", 0, "Jmin: минимальный размер junk-пакета")
	jmax := fs.Int("jmax", 0, "Jmax: максимальный размер junk-пакета")
	h1 := fs.String("h1", "", "H1 magic-header: число или диапазон min-max")
	h2 := fs.String("h2", "", "H2 magic-header: число или диапазон min-max")
	h3 := fs.String("h3", "", "H3 magic-header: число или диапазон min-max")
	h4 := fs.String("h4", "", "H4 magic-header: число или диапазон min-max")
	fs.Parse(args)

	store := state.NewStore(*stateFlag)
	s, err := store.Read()
	if err != nil {
		return err
	}
	if !s.IsSeed {
		return fmt.Errorf("set-params запускается только на seed (конфиг раздаёт seed) — эта нода regular")
	}

	// obf-обход: задаём SNI → seed бампит политику, применяет свой I1 и раздаёт per-node
	// I1 всем нодам (active-push). I1 не рвёт туннель → strand невозможен.
	if *sni != "" {
		version, err := setObfPolicy(store, *sni, rand.Reader)
		if err != nil {
			return err
		}
		fmt.Printf("✓ obf-обход обновлён (версия %d) → seed раздаёт per-node I1 всем нодам (active-push, ретрай до подтверждения)\n", version)
		return nil
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	ov, err := collectOverrides(set, paramFlags{s1, s2, s3, s4, jc, jmin, jmax, h1, h2, h3, h4})
	if err != nil {
		return err
	}
	if *regenerate && !ov.empty() {
		return fmt.Errorf("--regenerate и явные поля params взаимоисключающи (regenerate задаёт весь набор сам)")
	}
	if !*regenerate && ov.empty() {
		return fmt.Errorf("нечего менять: задайте --s1..--s4/--jc/--jmin/--jmax/--h1..--h4, либо --regenerate, либо --sni")
	}

	var pending *state.PendingParams
	if _, err := store.Update(func(st *state.State) error {
		// Под локом, поверх свежих применённых params: незаданные поля сохраняем.
		params, err := buildParams(st.AwgParams, *regenerate, ov)
		if err != nil {
			return err
		}
		// Жёсткая проверка ДО анонса: невалидный набор положил бы Configure на КАЖДОЙ
		// ноде (params общие на весь mesh) — отказываем здесь, сеть не трогаем.
		if err := awgparams.Validate(params); err != nil {
			return fmt.Errorf("новый набор params невалиден (отказ до анонса): %w", err)
		}
		// NewPending — поверх версии И висящего Pending → строго монотонно (повторный
		// set-params поверх активного flip остаётся корректным).
		pending = mesh.NewPending(st.ParamsVersion, st.Pending, params)
		st.Pending = pending
		return nil
	}); err != nil {
		return err
	}

	fmt.Printf(`✓ flag-day анонсирован
  версия params:  %d → %d
  применить в:     будет назначено, когда ВСЕ ноды подтвердят приём (active-push + ack)
  раздача:         seed POST'ит Pending каждой ноде; flip синхронный, связь кратко прервётся в окне применения
`, s.ParamsVersion, pending.Version)
	return nil
}

// paramFlags — сырые указатели на flag-значения сетевых params (из flag.FlagSet).
type paramFlags struct {
	s1, s2, s3, s4 *int
	jc, jmin, jmax *int
	h1, h2, h3, h4 *string
}

// paramOverrides — поля сетевых params, заданные оператором ЯВНО (nil = не трогать).
type paramOverrides struct {
	s1, s2, s3, s4 *int
	jc, jmin, jmax *int
	h1, h2, h3, h4 *awgparams.HeaderRange
}

// empty — оператор не задал ни одного сетевого поля.
func (o paramOverrides) empty() bool {
	return o.s1 == nil && o.s2 == nil && o.s3 == nil && o.s4 == nil &&
		o.jc == nil && o.jmin == nil && o.jmax == nil &&
		o.h1 == nil && o.h2 == nil && o.h3 == nil && o.h4 == nil
}

// apply накладывает заданные поля поверх base; незаданные остаются как есть.
func (o paramOverrides) apply(base awgparams.Params) awgparams.Params {
	setInt(&base.S1, o.s1)
	setInt(&base.S2, o.s2)
	setInt(&base.S3, o.s3)
	setInt(&base.S4, o.s4)
	setInt(&base.Jc, o.jc)
	setInt(&base.Jmin, o.jmin)
	setInt(&base.Jmax, o.jmax)
	setHeader(&base.H1, o.h1)
	setHeader(&base.H2, o.h2)
	setHeader(&base.H3, o.h3)
	setHeader(&base.H4, o.h4)
	return base
}

func setInt(dst *int, src *int) {
	if src != nil {
		*dst = *src
	}
}

func setHeader(dst *awgparams.HeaderRange, src *awgparams.HeaderRange) {
	if src != nil {
		*dst = *src
	}
}

// collectOverrides выбирает из flag-значений только РЕАЛЬНО заданные (set[name]) поля,
// парся H-диапазоны. Так «флаг со значением 0 по умолчанию» не путается с «оператор
// явно задал 0».
func collectOverrides(set map[string]bool, f paramFlags) (paramOverrides, error) {
	var o paramOverrides
	if set["s1"] {
		o.s1 = f.s1
	}
	if set["s2"] {
		o.s2 = f.s2
	}
	if set["s3"] {
		o.s3 = f.s3
	}
	if set["s4"] {
		o.s4 = f.s4
	}
	if set["jc"] {
		o.jc = f.jc
	}
	if set["jmin"] {
		o.jmin = f.jmin
	}
	if set["jmax"] {
		o.jmax = f.jmax
	}
	for _, h := range []struct {
		name string
		raw  *string
		dst  **awgparams.HeaderRange
	}{
		{"h1", f.h1, &o.h1}, {"h2", f.h2, &o.h2}, {"h3", f.h3, &o.h3}, {"h4", f.h4, &o.h4},
	} {
		if !set[h.name] {
			continue
		}
		hr, err := parseHeaderRange(*h.raw)
		if err != nil {
			return paramOverrides{}, fmt.Errorf("--%s: %w", h.name, err)
		}
		*h.dst = &hr
	}
	return o, nil
}

// buildParams формирует новый набор: либо свежий случайный (--regenerate), либо
// override'ы поверх текущих применённых params.
func buildParams(current awgparams.Params, regenerate bool, ov paramOverrides) (awgparams.Params, error) {
	if regenerate {
		p, err := awgparams.Generate()
		if err != nil {
			return awgparams.Params{}, fmt.Errorf("generate params: %w", err)
		}
		return p, nil
	}
	return ov.apply(current), nil
}

// parseHeaderRange разбирает H-диапазон из CLI: "min-max" → {min,max}, "n" → {n,n}.
func parseHeaderRange(s string) (awgparams.HeaderRange, error) {
	if i := strings.IndexByte(s, '-'); i >= 0 {
		min, err1 := strconv.ParseUint(s[:i], 10, 32)
		max, err2 := strconv.ParseUint(s[i+1:], 10, 32)
		if err1 != nil || err2 != nil {
			return awgparams.HeaderRange{}, fmt.Errorf("неверный диапазон %q (ждём min-max)", s)
		}
		return awgparams.HeaderRange{Min: uint32(min), Max: uint32(max)}, nil
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return awgparams.HeaderRange{}, fmt.Errorf("неверное значение %q (ждём число или min-max)", s)
	}
	return awgparams.HeaderRange{Min: uint32(n), Max: uint32(n)}, nil
}

// setObfPolicy бампит obf-политику (новый SNI) монотонно и сразу применяет seed'у его
// СОБСТВЕННЫЙ per-node I1 (reconciler доведёт до device). Остальным нодам per-node I1
// раздаст seed-push-цикл (obfPusher). Возвращает новую версию политики.
func setObfPolicy(store *state.Store, sni string, rng io.Reader) (uint64, error) {
	i1, err := awgparams.GenerateQUICInitialObf(sni, rng)
	if err != nil {
		return 0, fmt.Errorf("generate obf I1: %w", err)
	}
	var version uint64
	if _, err := store.Update(func(st *state.State) error {
		version = 1
		if st.ObfPolicy != nil {
			version = st.ObfPolicy.Version + 1
		}
		st.ObfPolicy = &state.ObfPolicy{SNI: sni, Version: version}
		st.LocalObf.I1 = i1
		st.ObfVersion = version
		return nil
	}); err != nil {
		return 0, err
	}
	return version, nil
}
