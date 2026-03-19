// Package fipstls stubs crypto/internal/boring/fipstls for the crypto/tls fork.
package fipstls

// Required returns false — FIPS TLS is not enforced in the fork.
func Required() bool {
	return false
}

// Force is a no-op in the stub.
func Force() {}

// Abandon is a no-op in the stub.
func Abandon() {}
