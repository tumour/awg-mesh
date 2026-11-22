package wg

import (
	"strings"
	"testing"

	"github.com/tumour/awg-mesh/internal/awgparams"
	"github.com/tumour/awg-mesh/internal/state"
)

// TestWriteAwgParams фиксирует UAPI-формат AWG-2.0: H1-H4 как "min-max",
// S3/S4 шлются всегда (lib трактует 0 как «выключено»), а I1-I5 — только
// непустые (per-node CPS-пакеты). Формат сверен с amneziawg-go v3.1
// device/uapi.go (H через UintRange "min-max", s1-s4 uint16, i* через newObfChain).
func TestWriteAwgParams(t *testing.T) {
	p := awgparams.Params{
		Jc: 5, Jmin: 50, Jmax: 200, S1: 80, S2: 77, S3: 38, S4: 7,
		H1: awgparams.HeaderRange{Min: 100, Max: 200},
		H2: awgparams.HeaderRange{Min: 300, Max: 300}, // вырожденный → "300-300"
		H3: awgparams.HeaderRange{Min: 500, Max: 600},
		H4: awgparams.HeaderRange{Min: 700, Max: 800},
	}
	lo := awgparams.LocalObf{I1: "<r 64>", I3: "<b 0xdeadbeef>"} // I2/I4/I5 пусты

	out := NetParamsUAPI(p) + LocalObfUAPI(lo)

	mustHave := []string{
		"jc=5\n", "jmin=50\n", "jmax=200\n",
		"s1=80\n", "s2=77\n", "s3=38\n", "s4=7\n",
		"h1=100-200\n", "h2=300-300\n", "h3=500-600\n", "h4=700-800\n",
		"i1=<r 64>\n", "i3=<b 0xdeadbeef>\n",
	}
	for _, want := range mustHave {
		if !strings.Contains(out, want) {
			t.Errorf("UAPI output missing %q\n--- got ---\n%s", want, out)
		}
	}
	// Пустые I-пакеты не должны попадать в конфиг.
	for _, bad := range []string{"i2=", "i4=", "i5="} {
		if strings.Contains(out, bad) {
			t.Errorf("UAPI output must not contain empty %q\n--- got ---\n%s", bad, out)
		}
	}
}

// PeerUAPI — peer-блок, который meshd шлёт в device (его же используют interop-тесты,
// чтобы проверять РОВНО продуктовый формат, а не копию). Endpoint и keepalive — только
// у пира с public-endpoint; кривой ключ — ошибка, а не мусор в UAPI.
func TestPeerUAPI(t *testing.T) {
	pub := "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="
	hexKey := strings.Repeat("01", 32)

	got, err := PeerUAPI(state.Peer{PublicKey: pub, NodeIP: "100.64.0.5", Endpoint: "203.0.113.7:51820"})
	if err != nil {
		t.Fatal(err)
	}
	want := "public_key=" + hexKey + "\nallowed_ip=100.64.0.5/32\nendpoint=203.0.113.7:51820\npersistent_keepalive_interval=25\n"
	if got != want {
		t.Errorf("public-пир:\n got %q\nwant %q", got, want)
	}

	got, err = PeerUAPI(state.Peer{PublicKey: pub, NodeIP: "100.64.0.6"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "public_key=" + hexKey + "\nallowed_ip=100.64.0.6/32\n"; got != want {
		t.Errorf("NAT-пир:\n got %q\nwant %q", got, want)
	}

	for _, bad := range []string{"", "not-base64!", "AQID"} {
		if _, err := PeerUAPI(state.Peer{PublicKey: bad, NodeIP: "100.64.0.7"}); err == nil {
			t.Errorf("кривой ключ %q принят", bad)
		}
	}
}
