package handshake

import (
	"crypto/sha256"
	"hash"
)

// sha256Hash — фабрика hash.Hash для hkdf.New (требует func() hash.Hash).
func sha256Hash() hash.Hash { return sha256.New() }
