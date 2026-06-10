package handshake

import (
	"bytes"
	"testing"

	"github.com/tumour/awg-mesh/internal/wgkey"
)

func mustKeypair(t *testing.T) (wgkey.Private, wgkey.Public) {
	t.Helper()
	priv, err := wgkey.GeneratePrivate()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv, priv.Public()
}

// Полный IKpsk2-цикл: msg1 → msg2, потом транспортные сообщения в обе стороны
// через полученные CipherState'ы (порядок тот же, что в join.go/serve.go).
func TestHandshakeRoundTrip(t *testing.T) {
	cPriv, cPub := mustKeypair(t)
	sPriv, sPub := mustKeypair(t)
	psk, err := DerivePSK([]byte("cluster-secret"))
	if err != nil {
		t.Fatalf("derive psk: %v", err)
	}

	hi, err := InitiatorHandshake(cPriv[:], cPub[:], sPub[:], psk)
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	hr, err := ResponderHandshake(sPriv[:], sPub[:], psk)
	if err != nil {
		t.Fatalf("responder: %v", err)
	}

	msg1, _, _, err := hi.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("initiator msg1: %v", err)
	}
	if _, _, _, err := hr.ReadMessage(nil, msg1); err != nil {
		t.Fatalf("responder read msg1: %v", err)
	}

	msg2, srvC2S, srvS2C, err := hr.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("responder msg2: %v", err)
	}
	_, cliC2S, cliS2C, err := hi.ReadMessage(nil, msg2)
	if err != nil {
		t.Fatalf("initiator read msg2: %v", err)
	}

	// client → server
	ct, err := cliC2S.Encrypt(nil, nil, []byte("hello"))
	if err != nil {
		t.Fatalf("encrypt c2s: %v", err)
	}
	pt, err := srvC2S.Decrypt(nil, nil, ct)
	if err != nil {
		t.Fatalf("decrypt c2s: %v", err)
	}
	if !bytes.Equal(pt, []byte("hello")) {
		t.Fatalf("c2s mismatch: %q", pt)
	}

	// server → client
	ct, err = srvS2C.Encrypt(nil, nil, []byte("world"))
	if err != nil {
		t.Fatalf("encrypt s2c: %v", err)
	}
	pt, err = cliS2C.Decrypt(nil, nil, ct)
	if err != nil {
		t.Fatalf("decrypt s2c: %v", err)
	}
	if !bytes.Equal(pt, []byte("world")) {
		t.Fatalf("s2c mismatch: %q", pt)
	}
}

// Неправильный cluster-secret: msg1 проходит (psk2 подмешивает PSK только во
// втором сообщении), но initiator обязан упасть на чтении msg2.
func TestHandshakeWrongPSKFails(t *testing.T) {
	cPriv, cPub := mustKeypair(t)
	sPriv, sPub := mustKeypair(t)
	pskA, _ := DerivePSK([]byte("secret-a"))
	pskB, _ := DerivePSK([]byte("secret-b"))

	hi, err := InitiatorHandshake(cPriv[:], cPub[:], sPub[:], pskA)
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	hr, err := ResponderHandshake(sPriv[:], sPub[:], pskB)
	if err != nil {
		t.Fatalf("responder: %v", err)
	}

	msg1, _, _, err := hi.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("initiator msg1: %v", err)
	}
	if _, _, _, err := hr.ReadMessage(nil, msg1); err != nil {
		t.Fatalf("msg1 must pass (PSK not mixed yet): %v", err)
	}
	msg2, _, _, err := hr.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("responder msg2: %v", err)
	}
	if _, _, _, err := hi.ReadMessage(nil, msg2); err == nil {
		t.Fatal("handshake with mismatched PSK must fail at msg2")
	}
}

// Initiator пиннит не тот seed-pubkey (например, токен от другого mesh'а) —
// responder не сможет расшифровать msg1.
func TestHandshakeWrongSeedKeyFails(t *testing.T) {
	cPriv, cPub := mustKeypair(t)
	sPriv, sPub := mustKeypair(t)
	_, wrongPub := mustKeypair(t)
	psk, _ := DerivePSK([]byte("cluster-secret"))

	hi, err := InitiatorHandshake(cPriv[:], cPub[:], wrongPub[:], psk)
	if err != nil {
		t.Fatalf("initiator: %v", err)
	}
	hr, err := ResponderHandshake(sPriv[:], sPub[:], psk)
	if err != nil {
		t.Fatalf("responder: %v", err)
	}

	msg1, _, _, err := hi.WriteMessage(nil, nil)
	if err != nil {
		t.Fatalf("initiator msg1: %v", err)
	}
	if _, _, _, err := hr.ReadMessage(nil, msg1); err == nil {
		t.Fatal("msg1 pinned to wrong responder key must fail")
	}
}

func TestDerivePSKDeterministic(t *testing.T) {
	a1, err := DerivePSK([]byte("s"))
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	a2, _ := DerivePSK([]byte("s"))
	b, _ := DerivePSK([]byte("other"))

	if len(a1) != 32 {
		t.Fatalf("psk must be 32 bytes, got %d", len(a1))
	}
	if !bytes.Equal(a1, a2) {
		t.Fatal("same secret must derive same PSK")
	}
	if bytes.Equal(a1, b) {
		t.Fatal("different secrets must derive different PSKs")
	}
}
