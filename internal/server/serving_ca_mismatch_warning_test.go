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
}
