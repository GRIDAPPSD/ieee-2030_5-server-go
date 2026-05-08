package identity_test

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/identity"
)

func TestLFDI(t *testing.T) {
	cert := generateTestDeviceCert(t)
	lfdi := identity.LFDI(cert)

	if len(lfdi) != 40 {
		t.Errorf("LFDI length = %d, want 40 hex chars", len(lfdi))
	}

	for _, c := range lfdi {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
			t.Errorf("LFDI contains non-hex char: %c", c)
		}
	}
}

func TestSFDI(t *testing.T) {
	cert := generateTestDeviceCert(t)
	sfdi := identity.SFDI(cert)

	if len(sfdi) != 12 {
		t.Errorf("SFDI length = %d, want 12 decimal digits", len(sfdi))
	}

	for _, c := range sfdi {
		if c < '0' || c > '9' {
			t.Errorf("SFDI contains non-digit: %c", c)
		}
	}

	if !identity.ValidateSFDI(sfdi) {
		t.Errorf("SFDI %q fails checksum validation", sfdi)
	}
}

func TestSFDIChecksumValidation(t *testing.T) {
	tests := []struct {
		sfdi  string
		valid bool
	}{
		{"167261211391", true},
		{"167261211390", false},
		{"000000000000", true},
		{"12345", false},
	}

	for _, tt := range tests {
		got := identity.ValidateSFDI(tt.sfdi)
		if got != tt.valid {
			t.Errorf("ValidateSFDI(%q) = %v, want %v", tt.sfdi, got, tt.valid)
		}
	}
}

func TestSFDIConsistency(t *testing.T) {
	cert := generateTestDeviceCert(t)

	sfdi1 := identity.SFDI(cert)
	sfdi2 := identity.SFDI(cert)
	if sfdi1 != sfdi2 {
		t.Errorf("SFDI not deterministic: %q != %q", sfdi1, sfdi2)
	}

	lfdi1 := identity.LFDI(cert)
	lfdi2 := identity.LFDI(cert)
	if lfdi1 != lfdi2 {
		t.Errorf("LFDI not deterministic: %q != %q", lfdi1, lfdi2)
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	cert := generateTestDeviceCert(t)
	fp1 := identity.Fingerprint(cert)
	fp2 := identity.Fingerprint(cert)
	if fp1 != fp2 {
		t.Error("Fingerprint not deterministic")
	}
}

func TestFormatSFDI(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"167261211391", "167-261-211-391"},
		{"000000000000", "000-000-000-000"},
		{"short", "short"}, // pass-through for non-12-char input
	}
	for _, tt := range tests {
		got := identity.FormatSFDI(tt.in)
		if got != tt.want {
			t.Errorf("FormatSFDI(%q) = %q, want %q", tt.in, got, tt.want)
		}
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
