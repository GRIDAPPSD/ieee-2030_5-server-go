package server

// #638 fix round 1, MEDIUM 2: nothing previously compared the server's own
// leaf against the serving CA the banner advertises as this server's
// anchor. Pointing SEP2_SERVING_CA at a new CA while the leaf is still
// signed by the old one read as healthy; the device-side failure
// ("certificate signed by unknown authority") then has no boot-log signal
// pointing at the cause. servingCAMismatchWarning is a pure function, like
// its adminProxyWarning/metricsExposureWarning siblings, so this test
// drives it directly.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2cert"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

// pemEncodeCert wraps a DER-encoded certificate as a single PEM CERTIFICATE
// block, so generateIntermediateCA can hand back file-ready bytes the same
// shape certs.GenerateCA produces.
func pemEncodeCert(t *testing.T, der []byte) []byte {
	t.Helper()
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// generateIntermediateCA is #638 fix round 3 item 4's test scaffolding:
// internal/certs has no intermediate-CA constructor (GenerateCA is always
// self-signed), so this builds one directly with the stdlib, signed by
// parentCert/parentKey, to reproduce a root -> intermediate -> leaf chain.
func generateIntermediateCA(t *testing.T, cn string, parentCert *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate intermediate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("intermediate serial: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-1 * time.Minute),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, parentCert, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatalf("create intermediate certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse intermediate certificate: %v", err)
	}
	pemBytes := pemEncodeCert(t, der)
	return cert, key, pemBytes
}

// generateHardwareModuleLeaf builds a leaf shaped like the SunSpec test
// PKI's server certificates: empty Subject, a critical SAN holding only a
// HardwareModuleName, and no ExtKeyUsage. The SAN extension is copied from
// a certs.GenerateDeviceCert leaf so its encoding is the one this repo
// already mints and the stdlib leaves unhandled.
func generateHardwareModuleLeaf(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) []byte {
	t.Helper()
	devicePEM, _, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{HWSerialNum: "hmn-leaf"})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}
	device, err := certs.ParseCertificatePEM(devicePEM)
	if err != nil {
		t.Fatalf("parse device cert: %v", err)
	}
	var san *pkix.Extension
	for i, e := range device.Extensions {
		if e.Id.Equal(sep2cert.OIDSubjectAltName) {
			san = &device.Extensions[i]
		}
	}
	if san == nil || !san.Critical {
		t.Fatal("device cert has no critical SAN to copy")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:    big.NewInt(1101),
		NotBefore:       time.Now().Add(-1 * time.Minute),
		NotAfter:        time.Now().AddDate(1, 0, 0),
		KeyUsage:        x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement,
		ExtraExtensions: []pkix.Extension{*san},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf: %v", err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if len(parsed.UnhandledCriticalExtensions) == 0 {
		t.Fatal("leaf has no unhandled critical extension; the test would not exercise the fix")
	}
	return pemEncodeCert(t, der)
}

func TestServingCAMismatchWarning(t *testing.T) {
	dir := t.TempDir()

	servingCACertPEM, servingCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Serving CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(serving): %v", err)
	}
	servingCACert, err := certs.ParseCertificatePEM(servingCACertPEM)
	if err != nil {
		t.Fatalf("parse serving CA: %v", err)
	}
	servingCAKey, err := certs.ParseKeyPEM(servingCAKeyPEM)
	if err != nil {
		t.Fatalf("parse serving CA key: %v", err)
	}

	otherCACertPEM, otherCAKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Other CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(other): %v", err)
	}
	otherCACert, err := certs.ParseCertificatePEM(otherCACertPEM)
	if err != nil {
		t.Fatalf("parse other CA: %v", err)
	}
	otherCAKey, err := certs.ParseKeyPEM(otherCAKeyPEM)
	if err != nil {
		t.Fatalf("parse other CA key: %v", err)
	}

	matchingLeafPEM, _, err := certs.GenerateServerCert(servingCACert, servingCAKey, certs.ServerCertOptions{
		Hosts: []string{"localhost"}, CommonName: "638 matching leaf", ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert(matching): %v", err)
	}
	mismatchedLeafPEM, _, err := certs.GenerateServerCert(otherCACert, otherCAKey, certs.ServerCertOptions{
		Hosts: []string{"localhost"}, CommonName: "638 mismatched leaf", ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert(mismatched): %v", err)
	}

	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}
	matchingLeafFile := write("matching-leaf.crt", matchingLeafPEM)
	mismatchedLeafFile := write("mismatched-leaf.crt", mismatchedLeafPEM)
	missingLeafFile := filepath.Join(dir, "does-not-exist.crt")
	garbageLeafFile := write("garbage.crt", []byte("not a certificate"))

	t.Run("matching leaf: no warning", func(t *testing.T) {
		got := servingCAMismatchWarning(matchingLeafFile, servingCACert)
		if got != "" {
			t.Errorf("got warning for a leaf actually signed by the serving CA: %q", got)
		}
	})

	t.Run("mismatched leaf: WARNING naming both files and the failure mode", func(t *testing.T) {
		got := servingCAMismatchWarning(mismatchedLeafFile, servingCACert)
		if got == "" {
			t.Fatal("got no warning for a leaf signed by a DIFFERENT CA than the advertised serving CA")
		}
		for _, want := range []string{
			"WARNING",
			mismatchedLeafFile,
			"unknown authority",
			"SEP2_SERVING_CA",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("warning missing fragment %q\n---warning---\n%s", want, got)
			}
		}
	})

	t.Run("nil serving CA: no warning (nothing to compare against)", func(t *testing.T) {
		got := servingCAMismatchWarning(mismatchedLeafFile, nil)
		if got != "" {
			t.Errorf("got warning with a nil serving CA: %q", got)
		}
	})

	t.Run("unreadable leaf file: no warning, not a crash", func(t *testing.T) {
		got := servingCAMismatchWarning(missingLeafFile, servingCACert)
		if got != "" {
			t.Errorf("got warning for an unreadable leaf file: %q", got)
		}
	})

	t.Run("unparseable leaf file: no warning, not a crash", func(t *testing.T) {
		got := servingCAMismatchWarning(garbageLeafFile, servingCACert)
		if got != "" {
			t.Errorf("got warning for an unparseable leaf file: %q", got)
		}
	})

	// #638 fix round 3 item 4: root -> intermediate -> leaf, with certFile
	// holding the leaf followed by the intermediate (the layout LoadCAPair's
	// own doc comment says this repo's tooling produces). A direct-signature
	// check flags this as a mismatch even though the chain verifies; a
	// genuinely unrelated leaf (the control) must still warn, so this proves
	// the fix discriminates rather than simply warning less.
	intermediateCert, intermediateKey, intermediateCertPEM := generateIntermediateCA(t, "638 Item4 Intermediate CA", servingCACert, servingCAKey)
	chainedLeafPEM, _, err := certs.GenerateServerCert(intermediateCert, intermediateKey, certs.ServerCertOptions{
		Hosts: []string{"localhost"}, CommonName: "638 chained leaf", ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert(chained): %v", err)
	}
	chainedLeafFile := write("chained-leaf-plus-intermediate.crt", append(append([]byte{}, chainedLeafPEM...), intermediateCertPEM...))

	t.Run("chained leaf (leaf+intermediate file, root is the serving CA): no warning", func(t *testing.T) {
		got := servingCAMismatchWarning(chainedLeafFile, servingCACert)
		if got != "" {
			t.Errorf("got warning for a leaf that chains to the serving CA through an intermediate: %q", got)
		}
	})

	t.Run("control: an unrelated leaf still warns even with an intermediate in the file", func(t *testing.T) {
		got := servingCAMismatchWarning(mismatchedLeafFile, servingCACert)
		if got == "" {
			t.Fatal("got no warning for a leaf signed by an unrelated CA; the chain check can no longer fire")
		}
	})

	// A leaf with a critical HardwareModuleName-only SAN, like the SunSpec
	// test PKI's server certificates, used to warn "unhandled critical
	// extension" even when it chained to the serving CA. The same leaf from
	// an unrelated CA must still warn.
	hmnLeafFile := write("hmn-leaf.crt", generateHardwareModuleLeaf(t, servingCACert, servingCAKey))
	hmnMismatchedLeafFile := write("hmn-mismatched-leaf.crt", generateHardwareModuleLeaf(t, otherCACert, otherCAKey))

	t.Run("HardwareModuleName SAN leaf signed by the serving CA: no warning", func(t *testing.T) {
		got := servingCAMismatchWarning(hmnLeafFile, servingCACert)
		if got != "" {
			t.Errorf("got warning for a CSIP-shaped leaf signed by the serving CA: %q", got)
		}
	})

	t.Run("HardwareModuleName SAN leaf from an unrelated CA: still warns", func(t *testing.T) {
		got := servingCAMismatchWarning(hmnMismatchedLeafFile, servingCACert)
		if got == "" {
			t.Fatal("got no warning for a CSIP-shaped leaf signed by an unrelated CA")
		}
		if strings.Contains(got, "unhandled critical extension") {
			t.Errorf("warning still reports the acknowledged SAN: %q", got)
		}
	})
}
