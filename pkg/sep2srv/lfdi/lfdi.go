// Package lfdi is the one owner of what counts as an LFDI: the leading 20
// bytes of a certificate fingerprint, written as 40 hex digits (IEEE 2030.5
// section 6.3.4, HexBinary160). Callers differ in how much input they
// tolerate, so each tolerance is a separate function here rather than a
// private copy in the caller.
package lfdi

import "strings"

// Len is the number of hex digits in an LFDI.
const Len = 40

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func allHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isHexDigit(s[i]) {
			return false
		}
	}
	return true
}

// IsHex40 reports whether s is exactly 40 hex digits in either case.
func IsHex40(s string) bool {
	return len(s) == Len && allHex(s)
}

// IsCanonical reports whether s is exactly 40 upper-case hex digits, the
// only form that may key a store or name a file: no case folding, no
// whitespace, no padding.
func IsCanonical(s string) bool {
	return IsHex40(s) && s == strings.ToUpper(s)
}

// Canonical upper-cases s after stripping surrounding space, tab, CR and LF
// (the xs:hexBinary whitespace collapse), and reports whether the result is
// 40 hex digits.
func Canonical(s string) (string, bool) {
	s = strings.Trim(s, " \t\r\n")
	if !IsHex40(s) {
		return "", false
	}
	return strings.ToUpper(s), true
}

// CanonicalPadded validates a client-claimed deviceLFDI and restores its full
// HexBinary160 width. IEEE 2030.5 allows a shorter hex string that omits
// leading zero bytes, and the EPRI reference client's serializer drops them
// and lower-cases what remains, so a device whose certificate LFDI starts
// with a zero byte claims a shorter value than its own identity. Left-padding
// with '0' makes the claim compare equal to that identity again.
//
// raw must be non-empty, an even number of hex digits (a whole number of
// bytes), and at most 40; it is not trimmed.
func CanonicalPadded(raw string) (string, bool) {
	if raw == "" || len(raw) > Len || len(raw)%2 != 0 || !allHex(raw) {
		return "", false
	}
	return strings.ToUpper(strings.Repeat("0", Len-len(raw)) + raw), true
}
