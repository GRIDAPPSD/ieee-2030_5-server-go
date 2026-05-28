package tls_test

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls"
	"pgregory.net/rapid"
)

func TestLFDI(t *testing.T) {
	cert := generateTestDeviceCert(t)
	lfdi := sepTLS.LFDI(cert)

	if len(lfdi) != 40 {
		t.Errorf("LFDI length = %d, want 40 hex chars", len(lfdi))
	}

	// Verify it's valid hex
	for _, c := range lfdi {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
			t.Errorf("LFDI contains non-hex char: %c", c)
		}
	}
}

func TestSFDI(t *testing.T) {
	cert := generateTestDeviceCert(t)
	sfdi := sepTLS.SFDI(cert)

	if len(sfdi) != 12 {
		t.Errorf("SFDI length = %d, want 12 decimal digits", len(sfdi))
	}

	// Verify it's all digits
	for _, c := range sfdi {
		if c < '0' || c > '9' {
			t.Errorf("SFDI contains non-digit: %c", c)
		}
	}

	// Verify checksum: sum of all digits mod 10 == 0
	if !sepTLS.ValidateSFDI(sfdi) {
		t.Errorf("SFDI %q fails checksum validation", sfdi)
	}
}

func TestSFDIChecksumValidation(t *testing.T) {
	tests := []struct {
		sfdi  string
		valid bool
	}{
		{"167261211391", true},  // spec example
		{"167261211390", false}, // wrong checksum
		{"000000000000", true},  // all zeros valid (sum=0, 0 mod 10 = 0)
		{"12345", false},        // too short
		{"1234567890ab", false}, // 12 chars but contains non-digit runes
		{"abcdefghijkl", false}, // all non-digit runes, 12 chars
	}

	for _, tt := range tests {
		got := sepTLS.ValidateSFDI(tt.sfdi)
		if got != tt.valid {
			t.Errorf("ValidateSFDI(%q) = %v, want %v", tt.sfdi, got, tt.valid)
		}
	}
}

func TestSFDIConsistency(t *testing.T) {
	cert := generateTestDeviceCert(t)

	// Same cert should always produce same SFDI
	sfdi1 := sepTLS.SFDI(cert)
	sfdi2 := sepTLS.SFDI(cert)
	if sfdi1 != sfdi2 {
		t.Errorf("SFDI not deterministic: %q != %q", sfdi1, sfdi2)
	}

	// Same cert should always produce same LFDI
	lfdi1 := sepTLS.LFDI(cert)
	lfdi2 := sepTLS.LFDI(cert)
	if lfdi1 != lfdi2 {
		t.Errorf("LFDI not deterministic: %q != %q", lfdi1, lfdi2)
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	cert := generateTestDeviceCert(t)
	fp1 := sepTLS.Fingerprint(cert)
	fp2 := sepTLS.Fingerprint(cert)
	if fp1 != fp2 {
		t.Error("Fingerprint not deterministic")
	}
}

func TestFormatSFDI(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"167261211391", "167-261-211-391"},
		{"000000000000", "000-000-000-000"},
		{"123456789012", "123-456-789-012"},
		// non-12-length inputs pass through unchanged (documented behaviour)
		{"12345", "12345"},
		{"", ""},
	}
	for _, tt := range tests {
		got := sepTLS.FormatSFDI(tt.input)
		if got != tt.want {
			t.Errorf("FormatSFDI(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatSFDIRoundTrip(t *testing.T) {
	// FormatSFDI should produce a 15-char hyphenated string for a valid 12-digit SFDI;
	// stripping hyphens must recover the original.
	cert := generateTestDeviceCert(t)
	sfdi := sepTLS.SFDI(cert)
	formatted := sepTLS.FormatSFDI(sfdi)

	if len(formatted) != 15 {
		t.Errorf("FormatSFDI length = %d, want 15", len(formatted))
	}

	stripped := formatted[0:3] + formatted[4:7] + formatted[8:11] + formatted[12:15]
	if stripped != sfdi {
		t.Errorf("stripping hyphens from %q recovered %q, want %q", formatted, stripped, sfdi)
	}
}

// TestPropSFDIChecksumLaw is a property test (plan-4, IEEE-116).
//
// Property: for any device certificate, SFDI(cert) always produces a valid
// SFDI string — i.e. ValidateSFDI(SFDI(cert)) == true for all inputs.
//
// The CA is generated once and shared across iterations; only the device cert
// (varying HWSerialNum) is generated per iteration to keep the test fast.
//
// TDD shape note: this is regression-pinning, not driving new behaviour. The
// SFDI checksum law has held since day one; the property documents the
// invariant and acts as a tripwire for future regressions. It passes on first
// run. See plan-4-property-based-testing/plan.md, decision 2 (pinned
// RAPID_SEED for deterministic PR-gate failures).
func TestPropSFDIChecksumLaw(t *testing.T) {
	// Pre-generate CA once outside the rapid loop: only device-cert key
	// generation varies per iteration.
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Prop Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	caBlock, _ := pem.Decode(caCertPEM)
	caCert, _ := x509.ParseCertificate(caBlock.Bytes)
	keyBlock, _ := pem.Decode(caKeyPEM)
	caKeyRaw, _ := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	caKey := caKeyRaw.(*ecdsa.PrivateKey)

	rapid.Check(t, func(rt *rapid.T) {
		serial := rapid.StringN(1, 20, -1).Draw(rt, "serial")
		devCertPEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
			DeviceType:  certs.DeviceTypeGeneric,
			HWSerialNum: serial,
		})
		if err != nil {
			rt.Skip() // skip if cert generation fails for this serial (e.g. empty string)
		}
		devBlock, _ := pem.Decode(devCertPEM)
		devCert, _ := x509.ParseCertificate(devBlock.Bytes)

		sfdi := sepTLS.SFDI(devCert)
		if !sepTLS.ValidateSFDI(sfdi) {
			rt.Errorf("ValidateSFDI(SFDI(cert)) = false for serial %q, sfdi %q", serial, sfdi)
		}
	})
}

// TestPropSFDIValidatorRejectsMalformed is a property test (plan-4, IEEE-116).
//
// Property: for any string that is not exactly 12 ASCII decimal digits,
// ValidateSFDI must return false. Three classes of malformed input are tested:
//   - Strings shorter than 12 runes
//   - Strings longer than 12 runes
//   - Strings of exactly 12 runes containing at least one non-digit rune
//
// TDD shape note: regression-pinning. Passes on first run.
func TestPropSFDIValidatorRejectsMalformed(t *testing.T) {
	// Generator for a non-digit rune (any Unicode rune outside '0'..'9').
	nonDigit := rapid.Custom(func(ct *rapid.T) rune {
		r := rapid.Rune().Draw(ct, "r")
		for r >= '0' && r <= '9' {
			r = rapid.Rune().Draw(ct, "r")
		}
		return r
	})

	// Generator for a 12-rune string that contains at least one non-digit.
	// Build 12 runes where position 0 is always a non-digit.
	twelveWithNonDigit := rapid.Custom(func(ct *rapid.T) string {
		first := nonDigit.Draw(ct, "first")
		rest := rapid.StringOfN(rapid.Rune(), 11, 11, -1).Draw(ct, "rest")
		return string(first) + rest
	})

	malformed := rapid.OneOf(
		rapid.StringN(0, 11, -1),  // too short (0–11 runes)
		rapid.StringN(13, 30, -1), // too long (13–30 runes)
		twelveWithNonDigit,        // exactly 12 runes but contains non-digit
	)

	rapid.Check(t, func(rt *rapid.T) {
		s := malformed.Draw(rt, "s")
		if sepTLS.ValidateSFDI(s) {
			rt.Errorf("ValidateSFDI(%q) = true, want false (malformed input)", s)
		}
	})
}

func generateTestDeviceCert(t *testing.T) *x509.Certificate {
	t.Helper()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	caBlock, _ := pem.Decode(caCertPEM)
	caCert, _ := x509.ParseCertificate(caBlock.Bytes)
	keyBlock, _ := pem.Decode(caKeyPEM)
	caKeyRaw, _ := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)

	devCertPEM, _, err := certs.GenerateDeviceCert(caCert, caKeyRaw.(*ecdsa.PrivateKey), certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST-001",
	})
	if err != nil {
		t.Fatal(err)
	}

	devBlock, _ := pem.Decode(devCertPEM)
	devCert, _ := x509.ParseCertificate(devBlock.Bytes)
	return devCert
}
