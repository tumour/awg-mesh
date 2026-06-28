package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
)

// validCur — заведомо валидный текущий набор params (база для override-тестов).
func validCur() awgparams.Params {
	return awgparams.Params{
		Jc: 5, Jmin: 40, Jmax: 120, S1: 30, S2: 40, S3: 20, S4: 8,
		H1: awgparams.HeaderRange{Min: 10, Max: 20},
		H2: awgparams.HeaderRange{Min: 30, Max: 40},
		H3: awgparams.HeaderRange{Min: 50, Max: 60},
		H4: awgparams.HeaderRange{Min: 70, Max: 80},
	}
}

func TestCmdSetParams(t *testing.T) {
	dir := t.TempDir()

	t.Run("--regenerate анонсирует flag-day (Pending версии+1, ApplyAt не назначен)", func(t *testing.T) {
		sf := filepath.Join(dir, "seed.json")
		if err := (&state.State{NodeLabel: "s", IsSeed: true, ParamsVersion: 2}).Save(sf); err != nil {
			t.Fatal(err)
		}
		if err := cmdSetParams([]string{"--state-file", sf, "--regenerate"}); err != nil {
			t.Fatalf("cmdSetParams --regenerate: %v", err)
		}
		s, err := state.Load(sf)
		if err != nil {
			t.Fatal(err)
		}
		if s.Pending == nil {
			t.Fatal("Pending не установлен")
		}
		if s.Pending.Version != 3 {
			t.Errorf("Version = %d, want 3 (current+1)", s.Pending.Version)
		}
		if !s.Pending.ApplyAt.IsZero() {
			t.Errorf("ApplyAt = %v должен быть нулевым (announced, не закоммичен)", s.Pending.ApplyAt)
		}
		if err := awgparams.Validate(s.Pending.Params); err != nil {
			t.Errorf("--regenerate выдал невалидный набор: %v", err)
		}
	})

	t.Run("--s4 меняет ТОЛЬКО S4, прочие поля — из текущих params", func(t *testing.T) {
		sf := filepath.Join(dir, "seeds4.json")
		cur := validCur()
		if err := (&state.State{NodeLabel: "s", IsSeed: true, ParamsVersion: 2, AwgParams: cur}).Save(sf); err != nil {
			t.Fatal(err)
		}
		if err := cmdSetParams([]string{"--state-file", sf, "--s4", "16"}); err != nil {
			t.Fatalf("cmdSetParams --s4: %v", err)
		}
		s, _ := state.Load(sf)
		if s.Pending == nil || s.Pending.Version != 3 {
			t.Fatalf("Pending не анонсирован: %+v", s.Pending)
		}
		p := s.Pending.Params
		if p.S4 != 16 {
			t.Errorf("S4 = %d, want 16 (override)", p.S4)
		}
		if p.S1 != cur.S1 || p.S3 != cur.S3 || p.Jc != cur.Jc || p.H1 != cur.H1 || p.H4 != cur.H4 {
			t.Errorf("незаданные поля не сохранены: %+v (было %+v)", p, cur)
		}
	})

	t.Run("--h2 как диапазон min-max применяется", func(t *testing.T) {
		sf := filepath.Join(dir, "seedh.json")
		if err := (&state.State{NodeLabel: "s", IsSeed: true, ParamsVersion: 1, AwgParams: validCur()}).Save(sf); err != nil {
			t.Fatal(err)
		}
		// H2 [30,40] из validCur меняем на непересекающийся [100,150].
		if err := cmdSetParams([]string{"--state-file", sf, "--h2", "100-150"}); err != nil {
			t.Fatalf("cmdSetParams --h2: %v", err)
		}
		s, _ := state.Load(sf)
		if got := s.Pending.Params.H2; got.Min != 100 || got.Max != 150 {
			t.Fatalf("H2 = %v, want {100,150}", got)
		}
	})

	t.Run("невалидный набор отвергается ДО анонса (сеть не трогаем)", func(t *testing.T) {
		sf := filepath.Join(dir, "seedbad.json")
		if err := (&state.State{NodeLabel: "s", IsSeed: true, ParamsVersion: 1, AwgParams: validCur()}).Save(sf); err != nil {
			t.Fatal(err)
		}
		// 148+30 == 92+86 → padded init и response совпадут → Validate отвергает.
		if err := cmdSetParams([]string{"--state-file", sf, "--s1", "30", "--s2", "86"}); err == nil {
			t.Fatal("ожидалась ошибка валидации (брик всей сети)")
		}
		s, _ := state.Load(sf)
		if s.Pending != nil {
			t.Fatalf("невалидный набор не должен оставлять Pending: %+v", s.Pending)
		}
	})

	t.Run("кривой H-диапазон → ошибка, без Pending", func(t *testing.T) {
		sf := filepath.Join(dir, "seedhbad.json")
		if err := (&state.State{NodeLabel: "s", IsSeed: true, ParamsVersion: 1, AwgParams: validCur()}).Save(sf); err != nil {
			t.Fatal(err)
		}
		if err := cmdSetParams([]string{"--state-file", sf, "--h1", "abc"}); err == nil {
			t.Fatal("ожидалась ошибка парсинга H-диапазона")
		}
		s, _ := state.Load(sf)
		if s.Pending != nil {
			t.Fatalf("при ошибке парсинга Pending не должен появляться: %+v", s.Pending)
		}
	})

	t.Run("без полей/sni/regenerate — ошибка", func(t *testing.T) {
		sf := filepath.Join(dir, "seednoop.json")
		if err := (&state.State{NodeLabel: "s", IsSeed: true, ParamsVersion: 1, AwgParams: validCur()}).Save(sf); err != nil {
			t.Fatal(err)
		}
		if err := cmdSetParams([]string{"--state-file", sf}); err == nil {
			t.Fatal("ожидалась ошибка: нечего менять")
		}
	})

	t.Run("--regenerate и явное поле взаимоисключающи", func(t *testing.T) {
		sf := filepath.Join(dir, "seedconflict.json")
		if err := (&state.State{NodeLabel: "s", IsSeed: true, ParamsVersion: 1, AwgParams: validCur()}).Save(sf); err != nil {
			t.Fatal(err)
		}
		if err := cmdSetParams([]string{"--state-file", sf, "--regenerate", "--s4", "16"}); err == nil {
			t.Fatal("ожидалась ошибка: regenerate + явные поля")
		}
	})

	t.Run("seed --sni задаёт obf-политику, бампит версию и применяет свой I1", func(t *testing.T) {
		sf := filepath.Join(dir, "seedobf.json")
		if err := (&state.State{NodeLabel: "s", IsSeed: true}).Save(sf); err != nil {
			t.Fatal(err)
		}
		if err := cmdSetParams([]string{"--state-file", sf, "--sni", "example.com"}); err != nil {
			t.Fatalf("cmdSetParams --sni: %v", err)
		}
		s, err := state.Load(sf)
		if err != nil {
			t.Fatal(err)
		}
		if s.ObfPolicy == nil || s.ObfPolicy.SNI != "example.com" || s.ObfPolicy.Version != 1 {
			t.Fatalf("obf-политика не задана корректно: %+v", s.ObfPolicy)
		}
		if s.ObfVersion != 1 {
			t.Errorf("ObfVersion = %d, want 1", s.ObfVersion)
		}
		// seed применяет СВОЙ I1 сразу — это валидный QUIC-мимик spec.
		if !strings.HasPrefix(s.LocalObf.I1, "<b 0x") {
			t.Fatalf("seed I1 не сгенерён: %q", s.LocalObf.I1)
		}

		// Повторный set-params --sni → версия монотонно растёт (1 → 2).
		if err := cmdSetParams([]string{"--state-file", sf, "--sni", "example.com"}); err != nil {
			t.Fatalf("второй cmdSetParams --sni: %v", err)
		}
		if s, _ = state.Load(sf); s.ObfPolicy.Version != 2 || s.ObfVersion != 2 {
			t.Fatalf("версия не бампнулась: policy=%+v obfVer=%d", s.ObfPolicy, s.ObfVersion)
		}
	})

	t.Run("regular-нода отказывает", func(t *testing.T) {
		rf := filepath.Join(dir, "reg.json")
		if err := (&state.State{NodeLabel: "r", IsSeed: false}).Save(rf); err != nil {
			t.Fatal(err)
		}
		if err := cmdSetParams([]string{"--state-file", rf, "--regenerate"}); err == nil {
			t.Fatal("ожидалась ошибка: set-params не на seed")
		}
	})
}
