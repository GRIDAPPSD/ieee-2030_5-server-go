package lfdi

import (
	"strings"
	"testing"
)

const up = "0123456789ABCDEF0123456789ABCDEF01234567"

func TestChecks(t *testing.T) {
	low := strings.ToLower(up)
	cases := []struct {
		name             string
		in               string
		hex40, canonical bool
		trimmed          string
		trimmedOK        bool
		padded           string
		paddedOK         bool
	}{
		{"upper", up, true, true, up, true, up, true},
		{"lower", low, true, false, up, true, up, true},
		{"mixed", up[:20] + low[20:], true, false, up, true, up, true},
		{"empty", "", false, false, "", false, "", false},
		{"39 digits", up[:39], false, false, "", false, "", false},
		{"41 digits", up + "0", false, false, "", false, "", false},
		{"non-hex", strings.Repeat("G", 40), false, false, "", false, "", false},
		{"surrounded by whitespace", " \t" + up + "\r\n", false, false, up, true, "", false},
		{"unicode space is not trimmed", "\u00a0" + up, false, false, "", false, "", false},
		{"newline inside", up[:20] + "\n" + up[21:], false, false, "", false, "", false},
		{"nul inside", up[:20] + "\x00" + up[21:], false, false, "", false, "", false},
		{"even short pads", "aabb", false, false, "", false, strings.Repeat("0", 36) + "AABB", true},
		{"odd short refused", "aab", false, false, "", false, "", false},
		{"short non-hex refused", "zz", false, false, "", false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsHex40(c.in); got != c.hex40 {
				t.Errorf("IsHex40 = %v; want %v", got, c.hex40)
			}
			if got := IsCanonical(c.in); got != c.canonical {
				t.Errorf("IsCanonical = %v; want %v", got, c.canonical)
			}
			if got, ok := Canonical(c.in); got != c.trimmed || ok != c.trimmedOK {
				t.Errorf("Canonical = %q, %v; want %q, %v", got, ok, c.trimmed, c.trimmedOK)
			}
			if got, ok := CanonicalPadded(c.in); got != c.padded || ok != c.paddedOK {
				t.Errorf("CanonicalPadded = %q, %v; want %q, %v", got, ok, c.padded, c.paddedOK)
			}
		})
	}
}
