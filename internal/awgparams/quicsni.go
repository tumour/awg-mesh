package awgparams

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// Генератор CPS-пакета I1, мимикрирующего под QUIC v1 Initial с заданным SNI.
//
// Зачем: стейтфул-DPI глушит устойчивый AWG-UDP-поток к «подозрительным» хостинг-AS:
// handshake проходит, поток через минуту умирает. Обычный random-I1 не спасает — но
// если ПЕРВЫМ пакетом отправить валидный QUIC Initial, внутри которого зашифрован
// ClientHello с allowlisted-SNI, DPI читает SNI (Initial-секреты выводятся из публичного
// DCID), считает поток легитимным QUIC к разрешённому домену и не душит. Проверено
// вживую: устойчивый дальний поток (240/240 за 5 мин).
//
// Это GENERIC-генератор: домен приходит параметром, в коде НЕТ конкретных SNI/байт.
// Порт проверенного эталона (sageptr/mini_quic_generator), сверен байт-в-байт тестом.
//
// Формат вывода — AWG obf-spec из чередующихся литералов и random-заполнителей
// (`<b 0xHEX><r N>`): фиксированная QUIC-структура и SNI идут литералами, а
// высокоэнтропийные поля (TLS-random, AEAD-tag) — как `<r N>`, чтобы amneziawg-go
// генерил их заново на каждый пакет (нет статической сигнатуры).

// quicInitialSalt — соль QUIC v1 для вывода Initial-секретов (RFC 9001 §5.2).
var quicInitialSalt = []byte{
	0x38, 0x76, 0x2c, 0xf7, 0xf5, 0x59, 0x34, 0xb3, 0x4d, 0x17,
	0x9a, 0xe6, 0xa4, 0xc8, 0x0c, 0xad, 0xcc, 0xbb, 0x7f, 0x0a,
}

const (
	tlsRandomLen = 32 // длина ClientHello.random
	aeadTagLen   = 16 // AES-128-GCM tag
	hpSampleLen  = 16 // sample для header-protection (один AES-блок)
)

// GenerateQUICInitialObf собирает I1-spec для домена sni, беря случайность (1 байт DCID
// + 32 байта TLS-random) из rng. Разный rng → разные I1 (per-node уникальность, DPI не
// напишет один rule); одинаковый rng → одинаковый I1 (детерминизм ядра).
func GenerateQUICInitialObf(sni string, rng io.Reader) (string, error) {
	dcid := make([]byte, 1)
	if _, err := io.ReadFull(rng, dcid); err != nil {
		return "", fmt.Errorf("read dcid: %w", err)
	}
	tlsRandom := make([]byte, tlsRandomLen)
	if _, err := io.ReadFull(rng, tlsRandom); err != nil {
		return "", fmt.Errorf("read tls random: %w", err)
	}
	return quicInitialObf(sni, dcid, tlsRandom)
}

// quicInitialObf — детерминированное ядро: I1-spec из заданных sni, dcid и tlsRandom.
// Вынесено отдельно, чтобы тест мог сверить байт-в-байт с фиксированными входами.
func quicInitialObf(sni string, dcid, tlsRandom []byte) (string, error) {
	if len(dcid) == 0 {
		return "", fmt.Errorf("dcid must be non-empty")
	}
	if len(tlsRandom) != tlsRandomLen {
		return "", fmt.Errorf("tls random must be %d bytes, got %d", tlsRandomLen, len(tlsRandom))
	}

	clientHello := quicClientHello(sni, tlsRandom)
	payload := quicCryptoFrame(clientHello, 0)

	// cut: какие куски пакета литеральны (<b>), какие — random-заполнители (<r>).
	// Чётные индексы — литералы, нечётные — random. Поля видит quicToAWG.
	dataOffset := len(payload) - len(clientHello)
	cut := []int{dataOffset + 6, tlsRandomLen, len(clientHello) - 38, aeadTagLen}

	pkn := []byte{0x00}
	packet, err := quicInitialPacket(dcid, pkn, payload)
	if err != nil {
		return "", err
	}
	quicFixCut(cut, len(packet), len(pkn), len(payload))
	return quicToAWG(packet, cut), nil
}

// quicInitialPacket собирает зашифрованный QUIC v1 Initial: заголовок (long-header,
// пустые SCID/Token) + AEAD-зашифрованный payload, с header-protection. SCID/Token
// пусты — этого достаточно, чтобы DPI распознал QUIC Initial и вычитал SNI.
func quicInitialPacket(dcid, pkn, payload []byte) ([]byte, error) {
	padding := quicInitialPadding(len(pkn), len(payload))

	// Заголовок в открытом виде (до header-protection).
	header := []byte{0xC0 | byte(len(pkn)-1), 0x00, 0x00, 0x00, 0x01} // long Initial, version 1
	header = append(header, quicStr8(dcid)...)
	header = append(header, 0x00, 0x00) // SCID len = 0, Token len = 0
	header = append(header, quicVarint(len(pkn)+len(payload)+padding+aeadTagLen)...)
	header = append(header, pkn...)

	// Initial-секреты из публичного DCID (RFC 9001 §5.2).
	initSecret, err := hkdf.Extract(sha256.New, dcid, quicInitialSalt)
	if err != nil {
		return nil, fmt.Errorf("hkdf extract: %w", err)
	}
	clientSecret, err := quicExpandLabel(initSecret, "client in", sha256.Size)
	if err != nil {
		return nil, err
	}
	key, err := quicExpandLabel(clientSecret, "quic key", 16)
	if err != nil {
		return nil, err
	}
	iv, err := quicExpandLabel(clientSecret, "quic iv", 12)
	if err != nil {
		return nil, err
	}
	hp, err := quicExpandLabel(clientSecret, "quic hp", 16)
	if err != nil {
		return nil, err
	}

	// Nonce = IV XOR packet number (выровнен по правому краю).
	nonce := make([]byte, len(iv))
	copy(nonce, iv)
	for i := range pkn {
		nonce[len(nonce)-len(pkn)+i] ^= pkn[i]
	}

	plaintext := make([]byte, len(payload)+padding)
	copy(plaintext, payload)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes key: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	enc := gcm.Seal(nil, nonce, plaintext, header) // шифртекст || 16-байтный tag

	// Header protection (RFC 9001 §5.4): маска = AES-ECB(hp, sample); sample берётся со
	// сдвигом 4-pknLen от начала шифртекста, длиной 16.
	sampleOff := 4 - len(pkn)
	hpBlock, err := aes.NewCipher(hp)
	if err != nil {
		return nil, fmt.Errorf("aes hp: %w", err)
	}
	mask := make([]byte, hpSampleLen)
	hpBlock.Encrypt(mask, enc[sampleOff:sampleOff+hpSampleLen])
	header[0] ^= mask[0] & 0x0f // long-header: маскируются младшие 4 бита
	for i := range pkn {
		header[len(header)-len(pkn)+i] ^= mask[1+i]
	}

	return append(header, enc...), nil
}

// quicInitialPadding — сколько нулей дописать к payload, чтобы хвост (PN+payload+pad+tag)
// был не меньше 20 байт (нужно для выборки sample header-protection). Для нашего
// ClientHello payload и так длинный → почти всегда 0.
func quicInitialPadding(pknLen, payloadLen int) int {
	if min := 20 - pknLen - payloadLen - aeadTagLen; min > 0 {
		return min
	}
	return 0
}

// quicClientHello — минимальный TLS ClientHello, несущий только SNI: handshake-тип 0x01,
// 24-битная длина, legacy_version 0x0303, random(32), пустые session_id/cipher_suites,
// расширения с единственным SNI. Не полностью валиден для TLS, но достаточен, чтобы DPI
// распарсил SNI.
func quicClientHello(sni string, tlsRandom []byte) []byte {
	body := []byte{0x03, 0x03}
	body = append(body, tlsRandom...)
	body = append(body, 0x00, 0x00, 0x00, 0x00)
	body = append(body, quicStr16(quicTLSExtSNI(sni))...)

	out := make([]byte, 4+len(body))
	out[0] = 0x01 // ClientHello
	out[1] = byte(len(body) >> 16)
	out[2] = byte(len(body) >> 8)
	out[3] = byte(len(body))
	copy(out[4:], body)
	return out
}

// quicTLSExtSNI — расширение server_name (RFC 6066): один host_name (type 0).
func quicTLSExtSNI(sni string) []byte {
	name := quicStr16([]byte(sni)) // ServerName: len16 || host
	body := make([]byte, 3+len(name))
	binary.BigEndian.PutUint16(body[0:2], uint16(len(name)+1)) // ServerNameList длина
	body[2] = 0x00                                             // NameType = host_name
	copy(body[3:], name)
	return quicTLSExt(0x0000, body) // extension_type server_name
}

// quicTLSExt — TLS-расширение: type(2) || length(2) || content.
func quicTLSExt(code uint16, content []byte) []byte {
	out := make([]byte, 4+len(content))
	binary.BigEndian.PutUint16(out[0:2], code)
	binary.BigEndian.PutUint16(out[2:4], uint16(len(content)))
	copy(out[4:], content)
	return out
}

// quicCryptoFrame — QUIC CRYPTO-фрейм (type 0x06): type || offset || length || data.
func quicCryptoFrame(data []byte, offset int) []byte {
	out := []byte{0x06}
	out = append(out, quicVarint(offset)...)
	out = append(out, quicVarint(len(data))...)
	return append(out, data...)
}

// quicExpandLabel — TLS 1.3 HKDF-Expand-Label (RFC 8446 §7.1) с пустым context.
// Для length ≤ размера хэша эквивалентно одному HMAC, что и нужно QUIC-секретам.
func quicExpandLabel(secret []byte, label string, length int) ([]byte, error) {
	full := "tls13 " + label
	info := make([]byte, 0, 4+len(full))
	info = binary.BigEndian.AppendUint16(info, uint16(length))
	info = append(info, byte(len(full)))
	info = append(info, full...)
	info = append(info, 0x00) // пустой context
	out, err := hkdf.Expand(sha256.New, secret, string(info), length)
	if err != nil {
		return nil, fmt.Errorf("hkdf expand %q: %w", label, err)
	}
	return out, nil
}

// quicVarint — QUIC variable-length integer (RFC 9000 §16): 2 старших бита кодируют длину.
func quicVarint(x int) []byte {
	switch {
	case x < 0x40:
		return []byte{byte(x)}
	case x < 0x4000:
		b := binary.BigEndian.AppendUint16(nil, uint16(x))
		b[0] |= 0x40
		return b
	case x < 0x40000000:
		b := binary.BigEndian.AppendUint32(nil, uint32(x))
		b[0] |= 0x80
		return b
	default:
		b := binary.BigEndian.AppendUint64(nil, uint64(x))
		b[0] |= 0xC0
		return b
	}
}

// quicStr8 — байты с 1-байтным префиксом длины.
func quicStr8(b []byte) []byte {
	return append([]byte{byte(len(b))}, b...)
}

// quicStr16 — байты с 2-байтным (BE) префиксом длины.
func quicStr16(b []byte) []byte {
	return append(binary.BigEndian.AppendUint16(nil, uint16(len(b))), b...)
}

// quicFixCut доводит cut-настройки до валидности: первый литеральный кусок должен
// покрывать заголовок и быть достаточным для header-protection.
func quicFixCut(cut []int, packetLen, pknLen, payloadLen int) {
	if short := 20 - pknLen - cut[0]; short > 0 {
		cut[0] += short
		cut[1] -= short
	}
	cut[0] += packetLen - payloadLen - aeadTagLen
}

// quicToAWG кодирует пакет в AWG obf-spec: чётные куски cut — литералы `<b 0xHEX>`,
// нечётные — random-заполнители `<r N>` (amneziawg-go подставит свежий random).
func quicToAWG(packet []byte, cut []int) string {
	var sb strings.Builder
	literal, offset := true, 0
	for _, n := range cut {
		if n > 0 {
			if literal {
				fmt.Fprintf(&sb, "<b 0x%x>", packet[offset:offset+n])
			} else {
				fmt.Fprintf(&sb, "<r %d>", n)
			}
			offset += n
		}
		literal = !literal
	}
	return sb.String()
}
