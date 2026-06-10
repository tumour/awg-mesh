package awgparams

import "testing"

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
