// Package handshake — обёртка над flynn/noise для Noise_IKpsk2 (нашего bootstrap-pattern'а).
//
// IK pattern: Initiator знает Responder static public key заранее (из join-token'а).
// psk2: pre-shared key (cluster-secret-derived) подмешивается после второго token'а
// первого сообщения — без правильного PSK handshake падает на MAC-verify.
//
// Результат handshake: пара CipherState'ов (init→resp, resp→init) для последующих
// шифрованных сообщений через proto.{Read,Write}Message.
package handshake

import (
	"crypto/rand"
	"fmt"
	"io"

	"github.com/flynn/noise"
	"golang.org/x/crypto/hkdf"
)

// suite — Noise cipher-suite: Curve25519 ECDH + ChaCha20-Poly1305 + BLAKE2s.
// То же что использует WireGuard, та же что Tailscale control protocol.
var suite = noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashBLAKE2s)

// DerivePSK — выводит 32-байтовый PSK из cluster-secret через HKDF-SHA256.
// Info-string "awg-mesh/noise-psk/v1" фиксирует контекст — если в будущем
// поменяем pattern, info изменится → PSK станет другим → старые ноды не смогут
// подключаться к новым (намеренное breaking-изменение).
func DerivePSK(clusterSecret []byte) ([]byte, error) {
	r := hkdf.New(sha256Hash, clusterSecret, nil, []byte("awg-mesh/noise-psk/v1"))
	psk := make([]byte, 32)
	if _, err := io.ReadFull(r, psk); err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	return psk, nil
}

// InitiatorHandshake — handshake-state для клиента (joining ноды). Знает свой
// keypair и static public key seed'а.
func InitiatorHandshake(privKey, pubKey, peerStaticPub, psk []byte) (*noise.HandshakeState, error) {
	return noise.NewHandshakeState(noise.Config{
		CipherSuite:           suite,
		Random:                rand.Reader,
		Pattern:               noise.HandshakeIK,
		Initiator:             true,
		PresharedKey:          psk,
		PresharedKeyPlacement: 2,
		StaticKeypair:         noise.DHKey{Private: privKey, Public: pubKey},
		PeerStatic:            peerStaticPub,
	})
}

// ResponderHandshake — handshake-state для сервера (seed'а). Знает свой keypair,
// peer-pubkey узнает из первого сообщения.
func ResponderHandshake(privKey, pubKey, psk []byte) (*noise.HandshakeState, error) {
	return noise.NewHandshakeState(noise.Config{
		CipherSuite:           suite,
		Random:                rand.Reader,
		Pattern:               noise.HandshakeIK,
		Initiator:             false,
		PresharedKey:          psk,
		PresharedKeyPlacement: 2,
		StaticKeypair:         noise.DHKey{Private: privKey, Public: pubKey},
	})
}
