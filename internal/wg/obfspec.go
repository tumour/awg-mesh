package wg

import (
	"errors"
	"fmt"
	"strings"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun/tuntest"
)

// ValidateObfSpec проверяет CPS-spec I-пакета (I1-I5) настоящим парсером amneziawg-go:
// поднимает in-memory device (без TUN/сокетов/root) и скармливает spec через IpcSet.
// Свой парсер держать не стоит — формат тегов (<b>, <r>, <c>, <t>, ...) принадлежит
// upstream и разъедется. Нужен там, где spec приходит извне (оператор через set-params,
// push от seed): кривой spec, попавший в state, роняет Configure на следующем старте.
func ValidateObfSpec(spec string) error {
	if strings.TrimSpace(spec) == "" {
		return errors.New("obf spec пустой")
	}
	// Upstream-парсер молча принимает строку без единого тега (получается пустая цепочка
	// — I-пакет ни о чём). Для оператора это опечатка, а не намерение — отказываем.
	if !strings.Contains(spec, "<") {
		return errors.New("obf spec без тегов (ожидается <b 0x..>, <r N>, ...)")
	}
	dev := device.NewDevice(tuntest.NewChannelTUN().TUN(), conn.NewDefaultBind(),
		device.NewLogger(device.LogLevelSilent, "obf-validate: "))
	defer dev.Close()
	// IpcSet требует приватник до прочих полей; любой 32-байтный годится — сила не проверяется.
	cfg := "private_key=0000000000000000000000000000000000000000000000000000000000000001\n" +
		"i1=" + spec + "\n"
	if err := dev.IpcSet(cfg); err != nil {
		return fmt.Errorf("amneziawg-go отверг I1 spec: %w", err)
	}
	return nil
}
