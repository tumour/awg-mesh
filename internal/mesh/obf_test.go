package mesh

import "testing"

// TestShouldAdoptObf — приём seed-раздаваемого obf (per-node I1) монотонен по версии:
// принимаем строго более новую, чем уже применённая. Идемпотентность (та же версия не
// переприменяется) и защита от отката (старее — отвергаем) — обязательны.
func TestShouldAdoptObf(t *testing.T) {
	tests := []struct {
		name              string
		current, incoming uint64
		want              bool
	}{
		{"строго новее — принять", 3, 4, true},
		{"та же версия — отвергнуть (идемпотентность)", 4, 4, false},
		{"старее — отвергнуть (нет отката)", 5, 4, false},
		{"первый obf с нуля", 0, 1, true},
		{"нулевой incoming — отвергнуть", 3, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ShouldAdoptObf(tt.current, tt.incoming); got != tt.want {
				t.Fatalf("ShouldAdoptObf(%d, %d) = %v, want %v", tt.current, tt.incoming, got, tt.want)
			}
		})
	}
}
