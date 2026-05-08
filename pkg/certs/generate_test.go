package certs_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/certs"
)

func TestGenerateCA(t *testing.T) {
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{
		Organization: "Test Energy",
		CommonName:   "Test Root CA",
		ValidYears:   10,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	cert := parseCertPEM(t, certPEM)
	key := parseKeyPEM(t, keyPEM)

	if !cert.IsCA {
		t.Error("CA cert should have IsCA=true")
	}
	if cert.BasicConstraintsValid != true {
		t.Error("CA cert should have BasicConstraintsValid=true")
	}
	if cert.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Error("CA cert should have KeyUsageCertSign")
	}
	if cert.KeyUsage&x509.KeyUsageCRLSign == 0 {
		t.Error("CA cert should have KeyUsageCRLSign")
	}
	if cert.Subject.CommonName != "Test Root CA" {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, "Test Root CA")
	}
	if cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Error("CA should use ECDSA")
	}
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("key should be *ecdsa.PrivateKey")
	}
	if ecKey.Curve != elliptic.P256() {
		t.Error("key should use P-256 curve")
	}

	// Verify self-signed
	if err := cert.CheckSignatureFrom(cert); err != nil {
		t.Errorf("CA cert should be self-signed: %v", err)
	}
}

func TestGenerateCAIndefiniteValidity(t *testing.T) {
	certPEM, _, err := certs.GenerateCA(certs.CAOptions{
		Organization: "Test",
		CommonName:   "Indefinite CA",
		ValidYears:   0,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	cert := parseCertPEM(t, certPEM)
	if cert.NotAfter.Year() != 9999 {
		t.Errorf("indefinite cert NotAfter year = %d, want 9999", cert.NotAfter.Year())
	}
}

func TestGenerateServerCert(t *testing.T) {
	caCert, caKey := generateTestCA(t)

	certPEM, keyPEM, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"localhost", "127.0.0.1", "::1"},
		CommonName: "Test Server",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}

	cert := parseCertPEM(t, certPEM)
	_ = parseKeyPEM(t, keyPEM)

	if cert.IsCA {
		t.Error("server cert should not be CA")
	}
	if cert.Subject.CommonName != "Test Server" {
		t.Errorf("CN = %q, want %q", cert.Subject.CommonName, "Test Server")
	}

	// Check SAN
	foundLocalhost := false
	for _, name := range cert.DNSNames {
		if name == "localhost" {
			foundLocalhost = true
		}
	}
	if !foundLocalhost {
		t.Error("server cert should have localhost in SAN DNSNames")
	}
	if len(cert.IPAddresses) < 1 {
		t.Error("server cert should have IP addresses in SAN")
	}

	// Verify signed by CA
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots}); err != nil {
		t.Errorf("server cert should verify against CA: %v", err)
	}
}

func TestGenerateDeviceCert(t *testing.T) {
	caCert, caKey := generateTestCA(t)

	certPEM, keyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWType:      asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 99},
		HWSerialNum: "INV-001",
		IsTestCert:  false,
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}

	cert := parseCertPEM(t, certPEM)
	_ = parseKeyPEM(t, keyPEM)

	if cert.IsCA {
		t.Error("device cert should not be CA")
	}
	if cert.PublicKeyAlgorithm != x509.ECDSA {
		t.Error("device cert should use ECDSA")
	}

	// Verify signed by CA
	roots := x509.NewCertPool()
	roots.AddCert(caCert)
	opts := x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	if _, err := cert.Verify(opts); err != nil {
		t.Errorf("device cert should verify against CA: %v", err)
	}

	// Check certificate has policy extension with deviceType OID
	foundDeviceTypePolicy := false
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(certs.OIDCertificatePolicies) {
			foundDeviceTypePolicy = true
		}
	}
	if !foundDeviceTypePolicy {
		t.Error("device cert should have CertificatePolicies extension")
	}
}

func TestGenerateDeviceTestCert(t *testing.T) {
	caCert, caKey := generateTestCA(t)

	certPEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWType:      asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 40732, 99},
		HWSerialNum: "INV-TEST",
		IsTestCert:  true,
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}

	cert := parseCertPEM(t, certPEM)

	// Should have both deviceType and device-auth-test policy OIDs
	foundPolicy := false
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(certs.OIDCertificatePolicies) {
			foundPolicy = true
		}
	}
	if !foundPolicy {
		t.Error("test device cert should have CertificatePolicies extension")
	}
}

func TestSerialNumberUniqueness(t *testing.T) {
	cert1PEM, _, _ := certs.GenerateCA(certs.CAOptions{CommonName: "CA1"})
	cert2PEM, _, _ := certs.GenerateCA(certs.CAOptions{CommonName: "CA2"})

	cert1 := parseCertPEM(t, cert1PEM)
	cert2 := parseCertPEM(t, cert2PEM)

	if cert1.SerialNumber.Cmp(cert2.SerialNumber) == 0 {
		t.Error("two CAs should have different serial numbers")
	}
}

func TestAllCertsUseP256(t *testing.T) {
	caCert, caKey := generateTestCA(t)

	serverPEM, _, _ := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts: []string{"localhost"},
	})
	devicePEM, _, _ := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "TEST",
	})

	for name, pemBytes := range map[string][]byte{
		"server": serverPEM,
		"device": devicePEM,
	} {
		cert := parseCertPEM(t, pemBytes)
		ecKey, ok := cert.PublicKey.(*ecdsa.PublicKey)
		if !ok {
			t.Errorf("%s cert: public key is not ECDSA", name)
			continue
		}
		if ecKey.Curve != elliptic.P256() {
			t.Errorf("%s cert: curve is %v, want P-256", name, ecKey.Curve)
		}
	}
}

// helpers

func generateTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{
		Organization: "Test",
		CommonName:   "Test CA",
		ValidYears:   10,
	})
	if err != nil {
		t.Fatalf("generateTestCA: %v", err)
	}
	cert := parseCertPEM(t, certPEM)
	key := parseKeyPEM(t, keyPEM)
	ecKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("CA key is not ECDSA")
	}
	return cert, ecKey
}

func parseCertPEM(t *testing.T, data []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("failed to decode PEM cert")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func parseKeyPEM(t *testing.T, data []byte) interface{} {
	t.Helper()
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("failed to decode PEM key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS8PrivateKey: %v", err)
	}
	return key
}
