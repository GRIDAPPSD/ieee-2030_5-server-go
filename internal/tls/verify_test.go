package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/certs"
)

// TestVerifyRejectsMalformedHardwareModuleSANInner exercises the M2 fail-closed
// contract on certHasHardwareModuleSAN: an otherName with the HardwareModuleName
// OID but garbage inside its [0] EXPLICIT value MUST be rejected. The
// function's docstring promises "any parse failure or mismatch means the SAN
// is NOT acknowledged"; this test holds it to that promise.
//
// Per Leon's PR #18 probe (finding M2).
func TestVerifyRejectsMalformedHardwareModuleSANInner(t *testing.T) {
	caCert, caKey := genCA(t)

	// Build a SAN whose otherName has the right outer OID but garbage inner
	// bytes — i.e. NOT a valid HardwareModuleName SEQUENCE.
	sanExt := mustBuildMalformedHMNSAN(t)

	deviceCert := genDeviceCertWithSANExt(t, caCert, caKey, sanExt)

	rawCerts := [][]byte{deviceCert.Raw}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	err := verifyClientCertWithHardwareModuleSAN(rawCerts, roots)
	if err == nil {
		t.Fatal("verify accepted cert with malformed HardwareModuleName SAN inner content; want rejection")
	}
}

// --- test helpers ---

// genCA creates a fresh root CA for use by these tests.
func genCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{
		Organization: "Test",
		CommonName:   "Verify Test CA",
		ValidYears:   10,
	})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}

	cBlock, _ := pem.Decode(certPEM)
	if cBlock == nil {
		t.Fatal("decode CA cert PEM")
	}
	cert, err := x509.ParseCertificate(cBlock.Bytes)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}

	kBlock, _ := pem.Decode(keyPEM)
	if kBlock == nil {
		t.Fatal("decode CA key PEM")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(kBlock.Bytes)
	if err != nil {
		t.Fatalf("parse CA key: %v", err)
	}
	ecKey, ok := keyAny.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("CA key is not ECDSA")
	}
	return cert, ecKey
}

// genDeviceCertWithSANExt mints a CA-signed leaf with the caller's SAN
// extension verbatim. Subject is empty; ExtKeyUsage = ClientAuth, matching
// CSIP device certs.
func genDeviceCertWithSANExt(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, sanExt pkix.Extension) *x509.Certificate {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		ExtraExtensions:       []pkix.Extension{sanExt},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return leaf
}

// mustBuildMalformedHMNSAN builds a SAN extension whose otherName has the
// HardwareModuleName OID but whose [0] EXPLICIT inner value is junk (not a
// valid HardwareModuleName SEQUENCE).
func mustBuildMalformedHMNSAN(t *testing.T) pkix.Extension {
	t.Helper()

	otherName := struct {
		TypeID asn1.ObjectIdentifier
		Value  asn1.RawValue
	}{
		TypeID: certs.OIDHardwareModuleName,
		Value: asn1.RawValue{
			Class:      asn1.ClassContextSpecific,
			Tag:        0,
			IsCompound: true,
			// Garbage inside the [0] EXPLICIT wrapper — definitely not a
			// valid HardwareModuleName SEQUENCE.
			Bytes: []byte{0xFF, 0xFF, 0xFF, 0xFF},
		},
	}

	otherNameBytes, err := asn1.MarshalWithParams(otherName, "tag:0")
	if err != nil {
		t.Fatalf("marshal OtherName: %v", err)
	}

	sanValue, err := asn1.Marshal(asn1.RawValue{
		Class:      asn1.ClassUniversal,
		Tag:        asn1.TagSequence,
		IsCompound: true,
		Bytes:      otherNameBytes,
	})
	if err != nil {
		t.Fatalf("marshal SAN: %v", err)
	}

	return pkix.Extension{
		Id:       certs.OIDSubjectAltName,
		Critical: true,
		Value:    sanValue,
	}
}
