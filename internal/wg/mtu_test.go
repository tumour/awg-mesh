package wg

import "testing"

// TestTunMTU фиксирует расчёт MTU awg0 с учётом s4-паддинга: цель path-MTU 1400,
// минус WG-оверхед 60, минус s4; пол minMTU=1280, потолок DefaultMTU=1420.
func TestTunMTU(t *testing.T) {
	cases := []struct {
		s4, want int
	}{
		{0, 1340},   // без s4
		{21, 1319},  // прод-значение: дало 583 Мбит/с РФ→загранка (было 0.7 при 1420)
		{27, 1313},  // верх рекомендованного диапазона s4
		{60, 1280},  // ровно упирается в пол
		{200, 1280}, // клампится на minMTU
	}
	for _, c := range cases {
		if got := TunMTU(c.s4); got != c.want {
			t.Errorf("TunMTU(%d) = %d, want %d", c.s4, got, c.want)
		}
	}
	// Для любого вменяемого s4 результат держится в [minMTU, DefaultMTU].
	for s4 := 0; s4 <= 64; s4++ {
		if m := TunMTU(s4); m < minMTU || m > 1420 {
			t.Errorf("TunMTU(%d) = %d вне [%d,1420]", s4, m, minMTU)
		}
	}
}
