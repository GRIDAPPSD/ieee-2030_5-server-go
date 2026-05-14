package tls_test

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls"
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
		{"167261211391", true},   // spec example
		{"167261211390", false},  // wrong checksum
		{"000000000000", true},   // all zeros valid (sum=0, 0 mod 10 = 0)
		{"12345", false},         // too short
		{"1234567890ab", false},  // 12 chars but contains non-digit runes
		{"abcdefghijkl", false},  // all non-digit runes, 12 chars
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
