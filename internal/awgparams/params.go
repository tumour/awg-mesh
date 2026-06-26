// Package awgparams — генерация и сериализация AmneziaWG-обфускационных
// параметров.
//
// Параметры делятся на две группы по тому, как amneziawg-go их использует
// (проверено по device/uapi.go + noise-protocol.go v0.2.19):
//
//   - Params — СЕТЕВЫЕ (flag-day): S1-S4 (padding init/response/cookie/transport)
//     и H1-H4 (magic-header диапазоны, message-type'ы). Применяются и на send,
//     и на receive (отправитель добавляет/паддит, получатель срезает/матчит),
//     поэтому ОБЯЗАНЫ совпадать у обеих сторон. Раздаются через bootstrap при
//     join, хранятся в state. Сюда же Jc/Jmin/Jmax — junk-пакеты; формально они
//     initiator-local, но раздаём как общий baseline (менять без flag-day всё
//     равно можно). Смена S/H — flag-day на всю сеть.
//   - LocalObf — ПО-НОДНЫЕ (initiator-local): I1-I5 (CPS-пакеты, маскирующие
//     старт потока под легитимный протокол — DNS/QUIC). Отправляются только
//     инициатором перед handshake, получатель их игнорит как мусор → совпадение
//     НЕ требуется, крутятся под конкретный ISP БЕЗ flag-day. НЕ раздаются.
package awgparams

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// HeaderRange — диапазон значений одного magic-header'а (H1..H4). amneziawg-go
// на каждый пакет выбирает случайное значение из [Min, Max] (device/magic-header.go
// GenSpec="min-max"). Min==Max → фиксированное значение (поведение AWG-1.0).
type HeaderRange struct {
	Min uint32 `json:"min"`
	Max uint32 `json:"max"`
}

// UnmarshalJSON читает H как объект {"min":..,"max":..} (схема v2) ИЛИ как
// одиночное число (схема v1) → вырожденный диапазон {n,n}. Это и есть бесшумная
// миграция v1→v2 на слое чтения: старый state.json дочитывается без отдельного
// migrate-кода, новые поля (s3/s4/local_obf) дефолтятся нулями штатным json.
func (h *HeaderRange) UnmarshalJSON(data []byte) error {
	// v2: объект. Если data — число, Unmarshal в структуру вернёт ошибку → ниже.
	var obj struct {
		Min *uint32 `json:"min"`
		Max *uint32 `json:"max"`
	}
	if err := json.Unmarshal(data, &obj); err == nil && obj.Min != nil && obj.Max != nil {
		h.Min, h.Max = *obj.Min, *obj.Max
		return nil
	}
	// v1: одиночное число → {n,n} (на проводе идентично старому фикс. header'у).
	var n uint32
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("header: want number (v1) or {min,max} (v2), got %s", data)
	}
	h.Min, h.Max = n, n
	return nil
}

// Params — СЕТЕВЫЕ AmneziaWG-параметры. ОДИНАКОВЫ на всех нодах mesh.
//
// Рекомендованные диапазоны (docs.amnezia.org + полевые гайды 2026):
//
//	Jc:    4-12    (количество junk-пакетов перед handshake)
//	Jmin:  40-80   (минимальный размер junk-пакета, байты)
//	Jmax:  Jmin+50..+250
//	S1:    15-150  (padding handshake-init;  init+S1 != response+S2)
//	S2:    15-150  (padding handshake-response)
//	S3:    8-55    (padding cookie-reply) — AWG-2.0
//	S4:    4-27    (padding КАЖДОГО transport-пакета) — AWG-2.0, ключ против
//	              flow-анализа всей сессии
//	H1-H4: непересекающиеся диапазоны uint32 в [5, 2^31-1] (safe-half для
//	              совместимости с Windows-клиентами)
type Params struct {
	Jc   int `json:"jc"`
	Jmin int `json:"jmin"`
	Jmax int `json:"jmax"`
	S1   int `json:"s1"`
	S2   int `json:"s2"`
	S3   int `json:"s3"`
	S4   int `json:"s4"`
	H1   HeaderRange `json:"h1"`
	H2   HeaderRange `json:"h2"`
	H3   HeaderRange `json:"h3"`
	H4   HeaderRange `json:"h4"`
}

// LocalObf — ПО-НОДНЫЕ initiator-local CPS-пакеты I1-I5 (obf-chain spec в
// синтаксисе amneziawg-go: "<b 0xHEX>" литерал, "<c>" счётчик, "<t>" timestamp,
// "<r N>" N случайных байт). Пустая строка = не слать соответствующий I-пакет.
// Подбираются эмпирически под путь/ISP (напр. QUIC-мимик до дата-центра).
type LocalObf struct {
	I1 string `json:"i1,omitempty"`
	I2 string `json:"i2,omitempty"`
	I3 string `json:"i3,omitempty"`
	I4 string `json:"i4,omitempty"`
	I5 string `json:"i5,omitempty"`
}

// DefaultI1 — дефолтный CPS-пакет (I1): мимикрия под QUIC Initial. Цель — чтобы
// ТСПУ по первым байтам потока классифицировал его как QUIC (разрешённый
// протокол), а не как VPN, и пропустил всю сессию. Раскладка пакета:
//
//	<b 0xc30000000108>  long-header, type=Initial, QUIC v1 (0x00000001), DCID-len=8
//	<r 8>               случайный DCID (8 байт)
//	<b 0x08>            SCID-len=8
//	<r 8>               случайный SCID
//	<b 0x0045dc>        token-len=0 + length-varint
//	<t>                 timestamp (packet-number/энтропия)
//	<r 16>              случайный payload
//
// Грамматика obf-chain amneziawg-go (device/obf.go): билдеры b/t/r/rc/rd; токены
// <r> генерят свежий рандом на КАЖДЫЙ пакет, так что два пакета не совпадают.
// I1 — initiator-local, получателем игнорируется → одинаковый дефолт на всех
// нодах безопасен; под упрямый ISP меняется отдельно (см. README/тюнинг).
const DefaultI1 = "<b 0xc30000000108><r 8><b 0x08><r 8><b 0x0045dc><t><r 16>"

// DefaultLocalObf — обфускация по умолчанию для новой ноды (init/join). Только
// I1 (QUIC-мимик); I2-I5 пусты — одного «первого впечатления» для DPI достаточно.
func DefaultLocalObf() LocalObf {
	return LocalObf{I1: DefaultI1}
}

// IsEmpty — у ноды не задан ни один из I1-I5 (нет initiator-обфускации). Так
// выглядят мигрированные с v1 ноды: миграция оставляла local_obf пустым ради
// wire-identical апгрейда. Демон backfill'ит таким DefaultLocalObf при старте.
func (o LocalObf) IsEmpty() bool {
	return o.I1 == "" && o.I2 == "" && o.I3 == "" && o.I4 == "" && o.I5 == ""
}

// Размеры стандартных WG-handshake-сообщений (amneziawg-go/device/noise-protocol.go:
// MessageInitiationSize=148, MessageResponseSize=92). Нужны для S1/S2-констрейнта ниже.
const (
	wgInitMsgSize     = 148
	wgResponseMsgSize = 92

	// safeHeaderMax — потолок magic-header'ов. Держим в нижней половине uint32
	// ([5, 2^31-1]): amneziawg-go под Windows исторически ломался на значениях
	// со старшим битом, и гайды 2026 рекомендуют генерить в safe-half.
	safeHeaderMax = 1<<31 - 1
)

// Generate возвращает СЕТЕВЫЕ Params со случайными AWG-2.0-значениями из
// рекомендованных диапазонов. Зовётся один раз на `meshd init`, затем раздаётся.
//
// Жёсткие констрейнты amneziawg-go (нарушение → IpcErrorInvalid на IpcSet →
// device.Configure падает → нода не поднимает awg0; а params общие на весь mesh,
// так что ломается ВСЯ сеть):
//   - padded init и response обязаны различаться по размеру:
//     wgInitMsgSize+S1 != wgResponseMsgSize+S2 (разница 56);
//   - каждый H-диапазон в [5, safeHeaderMax], Min<=Max, и диапазоны H1-H4
//     попарно НЕ пересекаются (иначе получатель не отличит message-type'ы).
func Generate() (Params, error) {
	jc, err := randIntRange(4, 12)
	if err != nil {
		return Params{}, err
	}
	jmin, err := randIntRange(40, 80)
	if err != nil {
		return Params{}, err
	}
	// Гарантируем Jmax > Jmin — иначе диапазон рандомизации junk-пакетов
	// схлопывается в фиксированный размер, обфускация теряет смысл.
	jmax, err := randIntRange(jmin+50, jmin+250)
	if err != nil {
		return Params{}, err
	}

	// S1/S2: перегенерируем, пока padded init и response не разойдутся по размеру.
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

	s3, err := randIntRange(8, 55)
	if err != nil {
		return Params{}, err
	}
	s4, err := randIntRange(4, 27)
	if err != nil {
		return Params{}, err
	}

	// H1-H4: непересекающиеся диапазоны. Ширина ~16-256; в пространстве 2^31
	// коллизии астрономически редки, так что цикл «перегенери при пересечении»
	// почти всегда проходит с первого раза.
	var hs [4]HeaderRange
	for {
		for i := range hs {
			if hs[i], err = randHeaderRange(); err != nil {
				return Params{}, err
			}
		}
		if validHeaderRanges(hs) {
			break
		}
	}

	return Params{
		Jc: jc, Jmin: jmin, Jmax: jmax,
		S1: s1, S2: s2, S3: s3, S4: s4,
		H1: hs[0], H2: hs[1], H3: hs[2], H4: hs[3],
	}, nil
}

// randHeaderRange — случайный H-диапазон [Min, Min+width] в safe-half, Min>4.
func randHeaderRange() (HeaderRange, error) {
	width, err := randIntRange(16, 256)
	if err != nil {
		return HeaderRange{}, err
	}
	min, err := randIntRange(5, safeHeaderMax-width)
	if err != nil {
		return HeaderRange{}, err
	}
	return HeaderRange{Min: uint32(min), Max: uint32(min + width)}, nil
}

// validHeaderRanges — каждый диапазон валиден (Min>4, Min<=Max<=safeHeaderMax)
// И диапазоны H1-H4 попарно не пересекаются.
func validHeaderRanges(hs [4]HeaderRange) bool {
	for i, h := range hs {
		if h.Min <= 4 || h.Min > h.Max || h.Max > safeHeaderMax {
			return false
		}
		for j := 0; j < i; j++ {
			// пересечение [a.Min,a.Max] и [b.Min,b.Max]
			if h.Min <= hs[j].Max && hs[j].Min <= h.Max {
				return false
			}
		}
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
