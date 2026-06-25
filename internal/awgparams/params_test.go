package awgparams

import "testing"

// amneziawg-go реджектит конфиг, если padded init и response одного размера
// (wgInitMsgSize+S1 == wgResponseMsgSize+S2). ~0.43% случайных пар коллидируют,
// поэтому 5000 прогонов без фикса почти наверняка поймали бы регресс (ожидание
// ~21 коллизия), а с фиксом — строго 0.
func TestGenerateS1S2NeverCollide(t *testing.T) {
	for i := 0; i < 5000; i++ {
		p, err := Generate()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if wgInitMsgSize+p.S1 == wgResponseMsgSize+p.S2 {
			t.Fatalf("init+S1 == response+S2 (%d): amneziawg-go would reject IpcSet; S1=%d S2=%d",
				wgInitMsgSize+p.S1, p.S1, p.S2)
		}
	}
}

func TestValidHeaderRanges(t *testing.T) {
	r := func(min, max uint32) HeaderRange { return HeaderRange{Min: min, Max: max} }
	cases := []struct {
		name string
		hs   [4]HeaderRange
		want bool
	}{
		{"distinct non-overlapping", [4]HeaderRange{r(5, 10), r(20, 30), r(40, 50), r(60, 70)}, true},
		{"single-value degenerate (v1-migrated)", [4]HeaderRange{r(5, 5), r(6, 6), r(7, 7), r(8, 8)}, true},
		{"contains reserved <=4", [4]HeaderRange{r(4, 10), r(20, 30), r(40, 50), r(60, 70)}, false},
		{"overlapping ranges", [4]HeaderRange{r(5, 25), r(20, 30), r(40, 50), r(60, 70)}, false},
		{"touching boundaries overlap", [4]HeaderRange{r(5, 20), r(20, 30), r(40, 50), r(60, 70)}, false},
		{"min>max", [4]HeaderRange{r(30, 20), r(40, 50), r(60, 70), r(80, 90)}, false},
		{"max beyond safe-half", [4]HeaderRange{r(5, 10), r(20, 30), r(40, 50), {Min: 60, Max: 1 << 31}}, false},
	}
	for _, c := range cases {
		if got := validHeaderRanges(c.hs); got != c.want {
			t.Errorf("%s: validHeaderRanges(%v) = %v, want %v", c.name, c.hs, got, c.want)
		}
	}
}

func TestGenerateWithinRecommendedRanges(t *testing.T) {
	for i := 0; i < 200; i++ {
		p, err := Generate()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if p.Jc < 4 || p.Jc > 12 {
			t.Fatalf("Jc out of range [4,12]: %d", p.Jc)
		}
		if p.Jmin < 40 || p.Jmin > 80 {
			t.Fatalf("Jmin out of range [40,80]: %d", p.Jmin)
		}
		if p.Jmax <= p.Jmin || p.Jmax > p.Jmin+250 {
			t.Fatalf("Jmax must be in (Jmin,Jmin+250]: jmin=%d jmax=%d", p.Jmin, p.Jmax)
		}
		if p.S1 < 15 || p.S1 > 150 {
			t.Fatalf("S1 out of range [15,150]: %d", p.S1)
		}
		if p.S2 < 15 || p.S2 > 150 {
			t.Fatalf("S2 out of range [15,150]: %d", p.S2)
		}
		if p.S3 < 8 || p.S3 > 55 {
			t.Fatalf("S3 out of range [8,55]: %d", p.S3)
		}
		if p.S4 < 4 || p.S4 > 27 {
			t.Fatalf("S4 out of range [4,27]: %d", p.S4)
		}
		// H1-H4 — валидные непересекающиеся диапазоны в safe-half.
		if !validHeaderRanges([4]HeaderRange{p.H1, p.H2, p.H3, p.H4}) {
			t.Fatalf("H1-H4 not valid non-overlapping ranges: %+v %+v %+v %+v",
				p.H1, p.H2, p.H3, p.H4)
		}
	}
}

// TestHeaderRangeUnmarshal — H читается и как объект (v2), и как число (v1).
func TestHeaderRangeUnmarshal(t *testing.T) {
	var v2 HeaderRange
	if err := v2.UnmarshalJSON([]byte(`{"min":100,"max":200}`)); err != nil || v2 != (HeaderRange{100, 200}) {
		t.Errorf("v2 object: got %+v err=%v, want {100,200}", v2, err)
	}
	var v1 HeaderRange
	if err := v1.UnmarshalJSON([]byte(`123456`)); err != nil || v1 != (HeaderRange{123456, 123456}) {
		t.Errorf("v1 number: got %+v err=%v, want {123456,123456}", v1, err)
	}
	var bad HeaderRange
	if err := bad.UnmarshalJSON([]byte(`"oops"`)); err == nil {
		t.Error("expected error on non-number/non-object header")
	}
}
