package tls

import (
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"strings"
)

// Fingerprint returns the SHA-256 hash of the DER-encoded certificate.
// This is the basis for both LFDI and SFDI derivation.
func Fingerprint(cert *x509.Certificate) [32]byte {
	return sha256.Sum256(cert.Raw)
}

// LFDI returns the Long Form Device Identifier: the first 20 bytes
// of the SHA-256 fingerprint, hex-encoded as 40 uppercase characters.
// Per spec section 6.3.4.
func LFDI(cert *x509.Certificate) string {
	fp := Fingerprint(cert)
	return fmt.Sprintf("%X", fp[:20])
}

// SFDI returns the Short Form Device Identifier: the certificate
// fingerprint left-truncated to 36 bits, expressed as 11 decimal digits
// with a sum-of-digits check digit appended (12 digits total).
// Per spec section 6.3.3.
func SFDI(cert *x509.Certificate) string {
	fp := Fingerprint(cert)

	// Left-truncate to 36 bits from the first 5 bytes.
	// Take top 36 bits: shift the 40-bit value right by 4.
	val := uint64(fp[0])<<28 |
		uint64(fp[1])<<20 |
		uint64(fp[2])<<12 |
		uint64(fp[3])<<4 |
		uint64(fp[4])>>4

	// Format as 11 decimal digits, zero-padded
	digits := fmt.Sprintf("%011d", val)

	// Compute check digit: sum of all 11 digits, then (10 - sum%10) % 10
	sum := 0
	for _, c := range digits {
		sum += int(c - '0')
	}
	checkDigit := (10 - sum%10) % 10

	return digits + fmt.Sprintf("%d", checkDigit)
}

// ValidateSFDI checks that an SFDI string has a valid check digit.
// The SFDI must be exactly 12 decimal digits, and the sum of all
// digits (including check digit) modulo 10 must equal 0.
func ValidateSFDI(sfdi string) bool {
	if len(sfdi) != 12 {
		return false
	}

	sum := 0
	for _, c := range sfdi {
		if c < '0' || c > '9' {
			return false
		}
		sum += int(c - '0')
	}
	return sum%10 == 0
}

// FormatSFDI formats a 12-digit SFDI with hyphens for display.
// e.g., "167261211391" -> "167-261-211-391"
func FormatSFDI(sfdi string) string {
	if len(sfdi) != 12 {
		return sfdi
	}
	parts := []string{sfdi[0:3], sfdi[3:6], sfdi[6:9], sfdi[9:12]}
	return strings.Join(parts, "-")
}
