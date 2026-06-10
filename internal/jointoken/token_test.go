package jointoken

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := Token{
		Secret:       "abcdefghijklmnopqrstuvwxyz234567abcdefghijklmnopqrst",
		SeedPubKey:   "c2VlZC1wdWJrZXk=",
		SeedEndpoint: "203.0.113.7:51820",
	}
	enc, err := Encode(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Токен копипастится в shell — должен быть однострочным и URL-safe
	if strings.ContainsAny(enc, "+/=\n ") {
		t.Fatalf("token not URL-safe: %q", enc)
	}

	out, err := Decode(enc)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out != in {
		t.Fatalf("round-trip mismatch:\n in: %+v\nout: %+v", in, out)
	}
}

func TestDecodeGarbage(t *testing.T) {
	if _, err := Decode("%%%not-base64%%%"); err == nil {
		t.Fatal("non-base64 must be rejected")
	}
	notJSON := base64.RawURLEncoding.EncodeToString([]byte("not json"))
	if _, err := Decode(notJSON); err == nil {
		t.Fatal("non-JSON must be rejected")
	}
}

func TestDecodeMissingFields(t *testing.T) {
	partial := base64.RawURLEncoding.EncodeToString([]byte(`{"secret":"x"}`))
	if _, err := Decode(partial); err == nil {
		t.Fatal("token without seed_pubkey/seed_endpoint must be rejected")
	}
}
