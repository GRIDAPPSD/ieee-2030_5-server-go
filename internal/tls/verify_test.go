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
	"strings"
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

// TestVerifyAcceptsCompliantDeviceCert is the canonical positive case: a
// CSIP-compliant device cert (critical HardwareModuleName SAN, valid chain to
// the configured root) must pass verification. (Dutch M1 / Leon table row 1.)
func TestVerifyAcceptsCompliantDeviceCert(t *testing.T) {
	caCert, caKey := genCA(t)

	deviceCertPEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "VERIFY-OK-001",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}
	deviceCert := mustParseCert(t, deviceCertPEM)

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	if err := verifyClientCertWithHardwareModuleSAN([][]byte{deviceCert.Raw}, roots); err != nil {
		t.Fatalf("verify rejected compliant device cert: %v", err)
	}
}

// TestVerifyRejectsEmptyRawCerts confirms the explicit empty-list guard.
// Under RequireAnyClientCert the stdlib handshake catches this first, but the
// belt-and-suspenders check must still return a specific error if called
// directly. (Dutch M1.)
func TestVerifyRejectsEmptyRawCerts(t *testing.T) {
	caCert, _ := genCA(t)
	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	err := verifyClientCertWithHardwareModuleSAN(nil, roots)
	if err == nil {
		t.Fatal("verify accepted empty rawCerts; want error")
	}
	if !strings.Contains(err.Error(), "no client certificate") {
		t.Errorf("unexpected error message: %v", err)
	}
}

// TestVerifyRejectsSANWithWrongOtherNameOID covers the off-by-one OID case
// Leon probed externally (1.3.6.1.5.5.7.8.5 instead of .8.4): the SAN is still
// listed as an unhandled critical extension because certHasHardwareModuleSAN
// returns false, so x509.Verify rejects the chain. (Dutch M1 / Leon row 4.)
func TestVerifyRejectsSANWithWrongOtherNameOID(t *testing.T) {
	caCert, caKey := genCA(t)

	wrongOID := asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 8, 5}
	sanExt := mustBuildCustomOtherNameSAN(t, wrongOID, true)
	deviceCert := genDeviceCertWithSANExt(t, caCert, caKey, sanExt)

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	err := verifyClientCertWithHardwareModuleSAN([][]byte{deviceCert.Raw}, roots)
	if err == nil {
		t.Fatal("verify accepted cert with off-by-one otherName OID; want rejection")
	}
}

// TestVerifyRejectsAdditionalUnknownCriticalExtension proves clearKnownCriticalSAN
// is selective: a cert with a valid HardwareModuleName SAN PLUS some other
// unknown critical extension still gets rejected, because only the SAN OID is
// removed from UnhandledCriticalExtensions. (Dutch M1 / Leon row 3 — the
// fail-closed proof.)
func TestVerifyRejectsAdditionalUnknownCriticalExtension(t *testing.T) {
	caCert, caKey := genCA(t)

	// Valid HardwareModuleName SAN.
	sanExt := mustBuildCustomOtherNameSAN(t, certs.OIDHardwareModuleName, true)

	// Plus an unrelated unknown critical extension.
	extraExt := pkix.Extension{
		Id:       asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 8675309},
		Critical: true,
		Value:    []byte{0x01, 0x02, 0x03},
	}

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
		ExtraExtensions:       []pkix.Extension{sanExt, extraExt},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	err = verifyClientCertWithHardwareModuleSAN([][]byte{leaf.Raw}, roots)
	if err == nil {
		t.Fatal("verify accepted cert with additional unknown critical extension; want rejection")
	}
}

// TestVerifyRejectsCertSignedByDifferentCA proves the chain walk still runs
// after the SAN-OID surgery: a cert signed by a different CA, even with a
// perfectly valid HardwareModuleName SAN, must fail x509.Verify. (Dutch M1 /
// Leon row 2.)
func TestVerifyRejectsCertSignedByDifferentCA(t *testing.T) {
	trustedCA, _ := genCA(t)

	// Mint device cert under a SECOND, untrusted CA.
	rogueCACertPEM, rogueCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		Organization: "Rogue",
		CommonName:   "Rogue CA",
		ValidYears:   10,
	})
	if err != nil {
		t.Fatalf("rogue CA: %v", err)
	}
	rogueCACert := mustParseCert(t, rogueCACertPEM)
	rogueCAKey := mustParseECKey(t, rogueCAKeyPEM)

	deviceCertPEM, _, err := certs.GenerateDeviceCert(rogueCACert, rogueCAKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "ROGUE-001",
	})
	if err != nil {
		t.Fatalf("rogue device cert: %v", err)
	}
	deviceCert := mustParseCert(t, deviceCertPEM)

	roots := x509.NewCertPool()
	roots.AddCert(trustedCA) // only trust the FIRST CA

	err = verifyClientCertWithHardwareModuleSAN([][]byte{deviceCert.Raw}, roots)
	if err == nil {
		t.Fatal("verify accepted cert signed by untrusted CA; want rejection")
	}
}

// TestVerifyRejectsExpiredCert confirms standard x509.Verify expiry enforcement
// is not short-circuited by our SAN-OID surgery. (Leon: fail-closed paths.)
func TestVerifyRejectsExpiredCert(t *testing.T) {
	caCert, caKey := genCA(t)

	sanExt := mustBuildCustomOtherNameSAN(t, certs.OIDHardwareModuleName, true)

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
		NotBefore:             time.Now().AddDate(-2, 0, 0),
		NotAfter:              time.Now().AddDate(-1, 0, 0), // expired one year ago
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

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	err = verifyClientCertWithHardwareModuleSAN([][]byte{leaf.Raw}, roots)
	if err == nil {
		t.Fatal("verify accepted expired cert; want rejection")
	}
}

// TestVerifyRejectsExtKeyUsageMismatch confirms KeyUsages: ExtKeyUsageClientAuth
// is enforced — a cert without ClientAuth fails verification. (Leon: fail-closed
// paths.)
func TestVerifyRejectsExtKeyUsageMismatch(t *testing.T) {
	caCert, caKey := genCA(t)

	sanExt := mustBuildCustomOtherNameSAN(t, certs.OIDHardwareModuleName, true)

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
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, // NOT ClientAuth
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

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	err = verifyClientCertWithHardwareModuleSAN([][]byte{leaf.Raw}, roots)
	if err == nil {
		t.Fatal("verify accepted cert without ExtKeyUsageClientAuth; want rejection")
	}
}

// TestVerifyRejectsSANWithExtraForeignOtherName proves clearKnownCriticalSAN
// does NOT acknowledge the SAN OID when the SAN contains a valid
// HardwareModuleName otherName entry plus an additional otherName with a
// different (foreign) OID. The acknowledgement contract is "every otherName
// in the SAN must be a well-formed HardwareModuleName"; an extra foreign
// otherName means the SAN as a whole still carries semantics Go's verifier
// intentionally treated as unhandled, so x509.Verify must reject the chain.
// (Copilot round 2 finding on internal/tls/verify.go:105.)
func TestVerifyRejectsSANWithExtraForeignOtherName(t *testing.T) {
	caCert, caKey := genCA(t)

	sanExt := mustBuildMultiOtherNameSAN(t, []otherNameSpec{
		{OID: certs.OIDHardwareModuleName, InnerKind: innerKindValidHMN},
		{OID: asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 8, 5}, InnerKind: innerKindArbitraryBytes},
	})
	deviceCert := genDeviceCertWithSANExt(t, caCert, caKey, sanExt)

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	err := verifyClientCertWithHardwareModuleSAN([][]byte{deviceCert.Raw}, roots)
	if err == nil {
		t.Fatal("verify accepted SAN with foreign otherName alongside HMN; want rejection")
	}
}

// TestVerifyRejectsSANWithExtraMalformedHMNOtherName covers the case where the
// SAN contains a valid HardwareModuleName otherName plus a second otherName
// that ALSO claims the HardwareModuleName OID but whose inner bytes are junk.
// Every otherName must parse as a well-formed HardwareModuleName for the SAN
// to be acknowledged; one bad apple spoils the bunch.
func TestVerifyRejectsSANWithExtraMalformedHMNOtherName(t *testing.T) {
	caCert, caKey := genCA(t)

	sanExt := mustBuildMultiOtherNameSAN(t, []otherNameSpec{
		{OID: certs.OIDHardwareModuleName, InnerKind: innerKindValidHMN},
		{OID: certs.OIDHardwareModuleName, InnerKind: innerKindGarbage},
	})
	deviceCert := genDeviceCertWithSANExt(t, caCert, caKey, sanExt)

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	err := verifyClientCertWithHardwareModuleSAN([][]byte{deviceCert.Raw}, roots)
	if err == nil {
		t.Fatal("verify accepted SAN with one good HMN plus one malformed HMN; want rejection")
	}
}

// TestVerifyRejectsSANWithCorruptOuterSequence covers the case where the
// SubjectAlternativeName extension value is NOT a well-formed GeneralNames
// SEQUENCE. In practice the stdlib's x509.ParseCertificate rejects such
// certs at parse time, but the verifier must still surface a clean error
// rather than crash or accept. This documents the fail-closed boundary.
func TestVerifyRejectsSANWithCorruptOuterSequence(t *testing.T) {
	caCert, caKey := genCA(t)

	// A critical SAN whose value bytes do not form a valid SEQUENCE.
	sanExt := pkix.Extension{
		Id:       certs.OIDSubjectAltName,
		Critical: true,
		// Garbage — not a valid SEQUENCE; the outer asn1.Unmarshal in
		// ExtractHardwareModuleName must fail and the SAN OID must remain
		// listed as unhandled critical even if the cert somehow parses.
		Value: []byte{0xFF, 0xFF, 0xFF, 0xFF},
	}

	der := mintRawCertDER(t, caCert, caKey, []pkix.Extension{sanExt})

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	err := verifyClientCertWithHardwareModuleSAN([][]byte{der}, roots)
	if err == nil {
		t.Fatal("verify accepted cert with corrupt SAN outer bytes; want rejection")
	}
}

// mintRawCertDER builds a CA-signed leaf with the given extra extensions and
// returns the raw DER (no parse step). Used by tests that need to feed certs
// to the verifier without going through x509.ParseCertificate first, e.g. to
// exercise corrupt-SAN paths the stdlib parser would otherwise reject.
func mintRawCertDER(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, extra []pkix.Extension) []byte {
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
		ExtraExtensions:       extra,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return der
}

// otherNameSpec describes a single otherName entry inside a SAN for tests
// that need to assemble multi-entry GeneralNames sequences.
type otherNameSpec struct {
	OID       asn1.ObjectIdentifier
	InnerKind innerKind
}

type innerKind int

const (
	innerKindValidHMN innerKind = iota
	innerKindGarbage
	innerKindArbitraryBytes
)

// mustBuildMultiOtherNameSAN assembles a SAN extension whose GeneralNames
// SEQUENCE contains the given otherName entries in order. Each entry's inner
// [0] EXPLICIT value is either a valid HardwareModuleName SEQUENCE, garbage,
// or arbitrary opaque bytes — selected by InnerKind.
func mustBuildMultiOtherNameSAN(t *testing.T, specs []otherNameSpec) pkix.Extension {
	t.Helper()

	var allOtherNames []byte
	for i, spec := range specs {
		var innerBytes []byte
		switch spec.InnerKind {
		case innerKindValidHMN:
			hmn := struct {
				HWType      asn1.ObjectIdentifier
				HWSerialNum asn1.RawValue
			}{
				HWType: certs.OIDIeee20305,
				HWSerialNum: asn1.RawValue{
					Class: asn1.ClassUniversal,
					Tag:   asn1.TagOctetString,
					Bytes: []byte("TEST-SN"),
				},
			}
			b, err := asn1.Marshal(hmn)
			if err != nil {
				t.Fatalf("spec[%d]: marshal HardwareModuleName: %v", i, err)
			}
			innerBytes = b
		case innerKindGarbage:
			innerBytes = []byte{0xFF, 0xFF, 0xFF, 0xFF}
		case innerKindArbitraryBytes:
			// Plausible-looking but spec-foreign payload.
			innerBytes = []byte{0x04, 0x04, 0xDE, 0xAD, 0xBE, 0xEF}
		}

		otherName := struct {
			TypeID asn1.ObjectIdentifier
			Value  asn1.RawValue
		}{
			TypeID: spec.OID,
			Value: asn1.RawValue{
				Class:      asn1.ClassContextSpecific,
				Tag:        0,
				IsCompound: true,
				Bytes:      innerBytes,
			},
		}

		otherNameBytes, err := asn1.MarshalWithParams(otherName, "tag:0")
		if err != nil {
			t.Fatalf("spec[%d]: marshal OtherName: %v", i, err)
		}
		allOtherNames = append(allOtherNames, otherNameBytes...)
	}

	sanValue, err := asn1.Marshal(asn1.RawValue{
		Class:      asn1.ClassUniversal,
		Tag:        asn1.TagSequence,
		IsCompound: true,
		Bytes:      allOtherNames,
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

// TestVerifyAcceptsCertWithoutSAN documents the acknowledge-only contract: the
// verifier does NOT enforce that a HardwareModuleName SAN is present. A
// well-signed cert with no SAN at all passes verification because there is no
// unhandled critical extension to trip. This matches the docstring on
// verifyClientCertWithHardwareModuleSAN (see PR #18 M1 doc clarification);
// enforcing the SAN's presence is intentionally a separate concern, handled
// at cert generation time. (Leon row 5 — empirical accept confirmed.)
func TestVerifyAcceptsCertWithoutSAN(t *testing.T) {
	caCert, caKey := genCA(t)

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
		Subject:               pkix.Name{CommonName: "no-SAN device"},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	if err := verifyClientCertWithHardwareModuleSAN([][]byte{leaf.Raw}, roots); err != nil {
		t.Fatalf("verify rejected no-SAN cert; the verifier is acknowledge-only and must accept (got: %v)", err)
	}
}

// mustBuildCustomOtherNameSAN builds a SAN extension with one otherName entry
// whose TypeID is the given OID and whose inner [0] EXPLICIT value is a valid
// HardwareModuleName SEQUENCE. Used by tests that need to vary the outer
// otherName OID while keeping the inner bytes well-formed.
func mustBuildCustomOtherNameSAN(t *testing.T, otherNameOID asn1.ObjectIdentifier, critical bool) pkix.Extension {
	t.Helper()

	hmn := struct {
		HWType      asn1.ObjectIdentifier
		HWSerialNum asn1.RawValue
	}{
		HWType: certs.OIDIeee20305,
		HWSerialNum: asn1.RawValue{
			Class: asn1.ClassUniversal,
			Tag:   asn1.TagOctetString,
			Bytes: []byte("TEST-SN"),
		},
	}
	hmnBytes, err := asn1.Marshal(hmn)
	if err != nil {
		t.Fatalf("marshal HardwareModuleName: %v", err)
	}

	otherName := struct {
		TypeID asn1.ObjectIdentifier
		Value  asn1.RawValue
	}{
		TypeID: otherNameOID,
		Value: asn1.RawValue{
			Class:      asn1.ClassContextSpecific,
			Tag:        0,
			IsCompound: true,
			Bytes:      hmnBytes,
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
		Critical: critical,
		Value:    sanValue,
	}
}

func mustParseCert(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("decode cert PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func mustParseECKey(t *testing.T, keyPEM []byte) *ecdsa.PrivateKey {
	t.Helper()
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatal("decode key PEM")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS8PrivateKey: %v", err)
	}
	ecKey, ok := keyAny.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("key is not ECDSA")
	}
	return ecKey
}
