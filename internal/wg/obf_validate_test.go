package wg

import (
	"strings"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"

	"github.com/tumour/awg-mesh/internal/awgparams"
)

// TestAwg2ConfigAcceptedByDevice — самый важный тест безопасности дефолта:
// прогоняет ПОЛНЫЙ AWG-2.0-конфиг (Generate() + DefaultLocalObf с QUIC-мимик I1)
// через настоящий amneziawg-go device.IpcSet на in-memory TUN (без root). Если
// дефолтный I1 или сгенерённые параметры невалидны — IpcSet вернёт ошибку, нода
// в проде не поднялась бы. Гоняем 100 раз: Generate() рандомизирован.
func TestAwg2ConfigAcceptedByDevice(t *testing.T) {
	for i := 0; i < 100; i++ {
		p, err := awgparams.Generate()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}

		tdev := tuntest.NewChannelTUN()
		dev := device.NewDevice(tdev.TUN(), conn.NewDefaultBind(),
			device.NewLogger(device.LogLevelSilent, "test: "))

		var sb strings.Builder
		// Любой валидный 32-байтный приватник (IpcSet парсит hex, силу не проверяет).
		sb.WriteString("private_key=0000000000000000000000000000000000000000000000000000000000000001\n")
		sb.WriteString("listen_port=0\n")
		writeAwgParams(&sb, p, awgparams.DefaultLocalObf())

		if err := dev.IpcSet(sb.String()); err != nil {
			dev.Close()
			t.Fatalf("amneziawg-go rejected AWG-2.0 config (Generate()+DefaultI1): %v\n%s", err, sb.String())
		}
		dev.Close()
	}
}

// TestApplyParamsAcceptedByDevice — flag-day reconfigure СЕТЕВЫХ params на лету
// (ApplyParams) принимается реальным amneziawg-go без пересоздания интерфейса.
// Гоняем смену набора (старт → flip) 50 раз: Generate() рандомизирован.
func TestApplyParamsAcceptedByDevice(t *testing.T) {
	for i := 0; i < 50; i++ {
		tdev := tuntest.NewChannelTUN()
		dev := &Device{dev: device.NewDevice(tdev.TUN(), conn.NewDefaultBind(),
			device.NewLogger(device.LogLevelSilent, "test: "))}

		start, _ := awgparams.Generate()
		flip, _ := awgparams.Generate()
		errStart := dev.ApplyParams(start) // как при старте
		errFlip := dev.ApplyParams(flip)   // flag-day flip на лету
		dev.dev.Close()

		if errStart != nil || errFlip != nil {
			t.Fatalf("ApplyParams rejected: start=%v flip=%v", errStart, errFlip)
		}
	}
}
