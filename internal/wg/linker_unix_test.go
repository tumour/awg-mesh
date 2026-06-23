//go:build !windows

package wg

import "testing"

// Регресс на БАГ #2 (прод v0.4.0): Delete должен быть идемпотентен и на iproute2
// (Debian), и на busybox (OpenWrt) — там разный текст про отсутствующий интерфейс.
func TestIfaceNotFound(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"iproute2", `Cannot find device "awg0"`, true},
		{"busybox", "ip: can't find device 'awg0'", true},
		{"busybox-no-prefix", "can't find device 'awg0'", true},
		{"permission denied", "RTNETLINK answers: Operation not permitted", false},
		{"empty", "", false},
		{"unrelated", "ip: bad command", false},
	}
	for _, c := range cases {
		if got := ifaceNotFound(c.out); got != c.want {
			t.Errorf("%s: ifaceNotFound(%q) = %v, want %v", c.name, c.out, got, c.want)
		}
	}
}
