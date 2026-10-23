// Package mrid is the one owner of what counts as a well-formed mRID on the
// admin and device-log surfaces: exactly 32 hex digits (IEEE 2030.5 mRIDType,
// 128 bits). internal/mrid mints them; this package only checks and
// canonicalizes input.
package mrid

import "strings"

// Len is the number of hex digits in an mRID.
const Len = 32

// Canonical accepts exactly Len hex digits, either case, and returns them
// uppercase, the form the issuer mints.
func Canonical(s string) (string, bool) {
	if len(s) != Len {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return "", false
		}
	}
	return strings.ToUpper(s), true
}
