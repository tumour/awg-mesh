// Package awgparams — генерация и сериализация AmneziaWG-обфускационных
// параметров (Jc, Jmin, Jmax, S1, S2, H1..H4).
//
// Эти параметры применяются на handshake-слое WireGuard для маскировки от DPI.
// ВАЖНО: все ноды в одной mesh-сети должны иметь ОДИНАКОВЫЕ параметры
// (кроме Jc/Jmin/Jmax, которые могут отличаться без потери совместимости).
// Поэтому генерим их один раз при `meshd init` и распространяем через bootstrap.
package awgparams

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
)

// Params — полный набор AmneziaWG-параметров.
//
// Рекомендованные диапазоны из docs.amnezia.org:
//
//	Jc:    4-12   (количество junk-пакетов перед handshake)
//	Jmin:  8-50   (минимальный размер junk-пакета, байты)
//	Jmax:  50-200 (максимальный размер junk-пакета)
//	S1:    15-150 (extra padding для handshake-init; init+S1 != response+S2)
//	S2:    15-150 (extra padding для handshake-response)
//	H1-H4: random uint32 > 4, все уникальные (кастомные WG-message-type'ы)
type Params struct {
	Jc   int    `json:"jc"`
	Jmin int    `json:"jmin"`
	Jmax int    `json:"jmax"`
	S1   int    `json:"s1"`
	S2   int    `json:"s2"`
	H1   uint32 `json:"h1"`
	H2   uint32 `json:"h2"`
	H3   uint32 `json:"h3"`
	H4   uint32 `json:"h4"`
}

// Размеры стандартных WG-handshake-сообщений (amneziawg-go/device/noise-protocol.go:
// MessageInitiationSize=148, MessageResponseSize=92). Нужны для S1/S2-констрейнта ниже.
const (
	wgInitMsgSize     = 148
	wgResponseMsgSize = 92
)

// Generate возвращает Params с разумными случайными значениями из реком. диапазонов.
//
// Два жёстких констрейнта amneziawg-go (нарушение → IpcErrorInvalid на IpcSet →
// device.Configure падает → нода не поднимает awg0; а params общие на весь mesh,
// так что ломается ВСЯ сеть):
//   - padded init и response пакеты обязаны различаться по размеру:
//     wgInitMsgSize+S1 != wgResponseMsgSize+S2 (~0.43% случайных пар из [15,150]²
//     коллидируют по S2-S1==56);
//   - H1-H4 уникальны И каждый > 4: для H ≤ 4 библиотека молча откатывается на
//     стандартный WG-message-type, теряя обфускацию этого типа.
func Generate() (Params, error) {
	jc, err := randIntRange(4, 12)
	if err != nil {
		return Params{}, err
	}
	jmin, err := randIntRange(8, 50)
	if err != nil {
		return Params{}, err
	}
	// Гарантируем Jmax > Jmin — иначе диапазон рандомизации junk-пакетов
	// схлопывается в фиксированный размер, обфускация теряет смысл.
	jmax, err := randIntRange(jmin+1, 200)
	if err != nil {
		return Params{}, err
	}
	// S1/S2: перегенерируем, пока padded init и response не разойдутся по размеру
	// (иначе amneziawg-go реджектит конфиг — см. констрейнт в доке Generate).
	var s1, s2 int
	for {
		if s1, err = randIntRange(15, 150); err != nil {
			return Params{}, err
		}
		if s2, err = randIntRange(15, 150); err != nil {
			return Params{}, err
		}
		if wgInitMsgSize+s1 != wgResponseMsgSize+s2 {
			break
		}
	}

	// H1-H4: уникальные И каждый > 4 (см. констрейнт в доке Generate).
	var hs [4]uint32
	for {
		for i := range hs {
			if hs[i], err = randUint32(); err != nil {
				return Params{}, err
			}
		}
		if validHeaders(hs) {
			break
		}
	}

	return Params{
		Jc: jc, Jmin: jmin, Jmax: jmax,
		S1: s1, S2: s2,
		H1: hs[0], H2: hs[1], H3: hs[2], H4: hs[3],
	}, nil
}

// validHeaders — все H уникальны И каждый > 4 (значения 0..4 amneziawg-go
// трактует как «использовать стандартный message-type», т.е. без обфускации).
func validHeaders(hs [4]uint32) bool {
	seen := make(map[uint32]bool, 4)
	for _, h := range hs {
		if h <= 4 || seen[h] {
			return false
		}
		seen[h] = true
	}
	return true
}

// randIntRange — случайное целое в диапазоне [min, max] включительно.
func randIntRange(min, max int) (int, error) {
	if min > max {
		return 0, fmt.Errorf("min > max: %d > %d", min, max)
	}
	span := uint32(max - min + 1)
	v, err := randUint32()
	if err != nil {
		return 0, err
	}
	return min + int(v%span), nil
}

func randUint32() (uint32, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}
