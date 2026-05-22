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
//   Jc:    4-12   (количество junk-пакетов перед handshake)
//   Jmin:  8-50   (минимальный размер junk-пакета, байты)
//   Jmax:  50-200 (максимальный размер junk-пакета)
//   S1:    15-150 (extra padding для handshake-init)
//   S2:    15-150 (extra padding для handshake-response)
//   H1-H4: random uint32, все уникальные (первые байты WG-message-type'ов)
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

// Generate возвращает Params с разумными случайными значениями из реком. диапазонов.
// Все H1-H4 гарантированно уникальны (требование AmneziaWG — иначе message-type'ы
// неотличимы друг от друга, handshake ломается).
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
	s1, err := randIntRange(15, 150)
	if err != nil {
		return Params{}, err
	}
	s2, err := randIntRange(15, 150)
	if err != nil {
		return Params{}, err
	}

	// H1-H4 должны быть уникальными. Перегенерируем до тех пор, пока все 4 различаются.
	// Вероятность коллизии в uint32 на 4 значениях ничтожна, но для корректности — цикл.
	var hs [4]uint32
	for {
		for i := range hs {
			hs[i], err = randUint32()
			if err != nil {
				return Params{}, err
			}
		}
		if hs[0] != hs[1] && hs[0] != hs[2] && hs[0] != hs[3] &&
			hs[1] != hs[2] && hs[1] != hs[3] && hs[2] != hs[3] {
			break
		}
	}

	return Params{
		Jc: jc, Jmin: jmin, Jmax: jmax,
		S1: s1, S2: s2,
		H1: hs[0], H2: hs[1], H3: hs[2], H4: hs[3],
	}, nil
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
