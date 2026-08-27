package auth

import (
	"crypto/rand"
	"encoding/hex"
)

// newRandomID returns n bytes from the system random source, hex-encoded.
//
// Shared by TicketStore and SessionStore because generating an opaque id is
// incidental boilerplate, not store semantics. The properties that must stay
// distinct between the two stores, one-time redemption versus non-consuming
// validation, live in their own types and are not affected by minting ids the
// same way.
func newRandomID(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
