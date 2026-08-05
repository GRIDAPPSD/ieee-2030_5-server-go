package auth

// Tests for extractSFDIPrefix (#13).
//
// Test pattern: PBT (pgregory.net/rapid) — plan-4 pilot precedent.
//
// Property (no-panic + contract):
//   - For any string s of length [0, 32], extractSFDIPrefix(s) never panics.
//   - If len(s) < 8, the function returns ("", non-nil error).
//   - If len(s) >= 8, the function returns (s[:8], nil).
//
// Plus two anchor example tests:
//   - s = "" (empty) → error, no panic.
//   - s = "12345" (5 chars) → error, no panic.

import (
	"testing"

	"pgregory.net/rapid"
)

// TestExtractSFDIPrefixEmpty is an anchor example test: empty string must
// return an error without panicking.
func TestExtractSFDIPrefixEmpty(t *testing.T) {
	_, err := extractSFDIPrefix("")
	if err == nil {
		t.Fatal("extractSFDIPrefix(\"\") should return non-nil error")
	}
}

// TestExtractSFDIPrefixShort is an anchor example test: a 5-char string is
// below the 8-char minimum and must return an error without panicking.
func TestExtractSFDIPrefixShort(t *testing.T) {
	_, err := extractSFDIPrefix("12345")
	if err == nil {
		t.Fatal("extractSFDIPrefix(\"12345\") should return non-nil error")
	}
}

// TestPropExtractSFDIPrefixNeverPanics is the primary PBT property.
// For any string of length 0..32:
//   - the call never panics
//   - len < 8  → returns ("", non-nil error)
//   - len >= 8 → returns (s[:8], nil)
func TestPropExtractSFDIPrefixNeverPanics(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		// Bound the string length to [0, 32] to keep the generator practical
		// while covering all interesting boundary regions (0, 7, 8, >8).
		s := rapid.StringOfN(rapid.Rune(), 0, 32, -1).Draw(rt, "sfdi")

		// The call must never panic — if it does, rapid catches it as a failure.
		prefix, err := extractSFDIPrefix(s)

		if len(s) < 8 {
			if err == nil {
				rt.Fatalf("len(s)=%d (<8): want non-nil error, got nil (prefix=%q)", len(s), prefix)
			}
			if prefix != "" {
				rt.Fatalf("len(s)=%d (<8): want empty prefix on error, got %q", len(s), prefix)
			}
		} else {
			if err != nil {
				rt.Fatalf("len(s)=%d (>=8): want nil error, got %v", len(s), err)
			}
			if prefix != s[:8] {
				rt.Fatalf("len(s)=%d: want prefix=%q, got %q", len(s), s[:8], prefix)
			}
		}
	})
}
