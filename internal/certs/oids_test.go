package certs_test

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/craig8/ieee-2030_5-go/internal/certs"
)

func TestHasPolicyOIDAdmin(t *testing.T) {
	caCert, caKey := mustGenerateTestCA(t)

	certPEM, _, err := certs.GenerateAdminCert(caCert, caKey, certs.AdminCertOptions{
		CommonName: "Test Admin",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	cert := mustParseCertPEM(t, certPEM)

	if !certs.HasPolicyOID(cert, certs.OIDPolicyAdmin) {
		t.Error("admin cert should have admin policy OID")
	}
	if certs.HasPolicyOID(cert, certs.OIDDeviceTypeGeneric) {
		t.Error("admin cert should NOT have device type OID")
	}
}

func TestHasPolicyOIDDevice(t *testing.T) {
	caCert, caKey := mustGenerateTestCA(t)

	certPEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST",
	})
	if err != nil {
		t.Fatal(err)
	}

	cert := mustParseCertPEM(t, certPEM)

	if certs.HasPolicyOID(cert, certs.OIDPolicyAdmin) {
		t.Error("device cert should NOT have admin policy OID")
	}
	if !certs.HasPolicyOID(cert, certs.OIDDeviceTypeGeneric) {
		t.Error("device cert should have generic device type OID")
	}
}

func TestGenerateAdminCert(t *testing.T) {
	caCert, caKey := mustGenerateTestCA(t)

	certPEM, _, err := certs.GenerateAdminCert(caCert, caKey, certs.AdminCertOptions{
		CommonName: "Grid Admin",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	cert := mustParseCertPEM(t, certPEM)

	if cert.Subject.CommonName != "Grid Admin" {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, "Grid Admin")
	}
	if cert.IsCA {
		t.Error("admin cert should not be CA")
	}

	// Verify signed by CA
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("admin cert should verify against CA: %v", err)
	}
}

func TestGenerateSelfSignedTLS(t *testing.T) {
	certPEM, keyPEM, err := certs.GenerateSelfSignedTLS([]string{"localhost", "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}

	cert := mustParseCertPEM(t, certPEM)

	if cert.Subject.CommonName != "IEEE 2030.5 Admin" {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, "IEEE 2030.5 Admin")
	}

	// Verify issuer == subject (self-signed)
	if cert.Issuer.CommonName != cert.Subject.CommonName {
		t.Errorf("issuer CN %q != subject CN %q (should be self-signed)",
			cert.Issuer.CommonName, cert.Subject.CommonName)
	}

	// Verify SAN
	foundLocalhost := false
	for _, name := range cert.DNSNames {
		if name == "localhost" {
			foundLocalhost = true
		}
	}
	if !foundLocalhost {
		t.Error("should have localhost in SAN")
	}
	if len(cert.IPAddresses) == 0 {
		t.Error("should have IP addresses in SAN")
	}

	// Verify key parses
	if keyPEM == nil {
		t.Fatal("key PEM should not be nil")
	}
}

func mustGenerateTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	cert := mustParseCertPEM(t, certPEM)
	key := mustParseKeyPEM(t, keyPEM)
	return cert, key
}

func mustParseCertPEM(t *testing.T, data []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("no PEM cert data")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func mustParseKeyPEM(t *testing.T, data []byte) *ecdsa.PrivateKey {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("no PEM key data")
	}
	raw, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	key, ok := raw.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("key is not ECDSA")
	}
	return key
}
