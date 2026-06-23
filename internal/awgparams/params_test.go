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

func TestValidHeaders(t *testing.T) {
	cases := []struct {
		name string
		hs   [4]uint32
		want bool
	}{
		{"all distinct and >4", [4]uint32{5, 6, 7, 8}, true},
		{"contains reserved 4", [4]uint32{4, 6, 7, 8}, false},
		{"contains reserved 0", [4]uint32{0, 6, 7, 8}, false},
		{"duplicate", [4]uint32{5, 5, 7, 8}, false},
		{"large distinct", [4]uint32{100, 200, 300, 400}, true},
	}
	for _, c := range cases {
		if got := validHeaders(c.hs); got != c.want {
			t.Errorf("%s: validHeaders(%v) = %v, want %v", c.name, c.hs, got, c.want)
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
		if p.Jmin < 8 || p.Jmin > 50 {
			t.Fatalf("Jmin out of range [8,50]: %d", p.Jmin)
		}
		if p.Jmax <= p.Jmin || p.Jmax > 200 {
			t.Fatalf("Jmax must be in (Jmin,200]: jmin=%d jmax=%d", p.Jmin, p.Jmax)
		}
		if p.S1 < 15 || p.S1 > 150 {
			t.Fatalf("S1 out of range [15,150]: %d", p.S1)
		}
		if p.S2 < 15 || p.S2 > 150 {
			t.Fatalf("S2 out of range [15,150]: %d", p.S2)
		}
		hs := [4]uint32{p.H1, p.H2, p.H3, p.H4}
		for a := 0; a < 4; a++ {
			for b := a + 1; b < 4; b++ {
				if hs[a] == hs[b] {
					t.Fatalf("H%d == H%d (%d) — message-types must be distinct", a+1, b+1, hs[a])
				}
			}
		}
	}
}
