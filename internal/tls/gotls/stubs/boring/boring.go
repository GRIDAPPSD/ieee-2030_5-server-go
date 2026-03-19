// Package boring stubs crypto/internal/boring for the crypto/tls fork.
// BoringCrypto is disabled in the fork.
package boring

import "crypto/cipher"

// Enabled reports whether BoringCrypto is available. Always false in the fork.
const Enabled = false

// Unreachable is a no-op. In the real boring package, it panics if
// BoringCrypto is enabled and execution reaches non-boring code paths.
func Unreachable() {}

// NewGCMTLS is never called when Enabled=false. Stub satisfies the interface.
func NewGCMTLS(c cipher.Block) (cipher.AEAD, error) {
	panic("boring: not available")
}
