package certs_test

// #638 fix round 1: LoadCAPair loads a CA certificate independently of its
// private key, so a deployment that keeps a certificate and removes its key
// still gets the certificate back (MEDIUM 1/3), and a certificate paired
// with a key that does not match it is treated as keyless rather than
// silently minting-ready (MEDIUM 2). Every case is proven against real
// generated key material, never a mocked pair.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

func rewrap(der []byte, width int) string {
	b64 := base64.StdEncoding.EncodeToString(der)
	var lines []string
	for i := 0; i < len(b64); i += width {
		end := i + width
		if end > len(b64) {
			end = len(b64)
		}
		lines = append(lines, b64[i:end])
	}
	return strings.Join(lines, "\n")
}

func genCertDER(t *testing.T) []byte {
	t.Helper()
	certPEM, _, err := certs.GenerateCA(certs.CAOptions{CommonName: "Fixture CA", ValidYears: 1})
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("generated CA PEM did not decode")
	}
	return block.Bytes
}

// TestFilterCertificatePEMPreservesNonStandardWrapping kills a mutant that
// substitutes pem.Encode (re-encoding at 64 columns) for a source-byte copy:
// a fixture wrapped at 48 columns, which Go's own encoder never produces,
// must come back exactly as wrapped, not renormalized to 64.
func TestFilterCertificatePEMPreservesNonStandardWrapping(t *testing.T) {
	der := genCertDER(t)
	src := "-----BEGIN CERTIFICATE-----\n" + rewrap(der, 48) + "\n-----END CERTIFICATE-----\n"

	got := certs.FilterCertificatePEM([]byte(src))
	if string(got) != src {
		t.Errorf("filtered output changed a 48-column fixture:\n got: %q\nwant: %q", got, src)
	}
	// Control: prove the assertion can fail. A re-encoded (64-column) form
	// of the identical certificate is NOT byte-equal to the 48-column
	// source, so this fixture does distinguish the two.
	var reencoded bytes.Buffer
	if err := pem.Encode(&reencoded, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatal(err)
	}
	if reencoded.String() == src {
		t.Fatal("control failed: re-encoded form is byte-identical to the 48-column source, fixture cannot detect a re-encode mutant")
	}
}

// TestFilterCertificatePEMDropsNonPKCS8PrivateKeyTypes kills a mutant that
// checks block.Type != "PRIVATE KEY" instead of block.Type == "CERTIFICATE":
// every other fixture in this suite uses PKCS8 ("PRIVATE KEY"), so a mutant
// keying off that one literal would still pass them. An EC-labeled key
// block is neither the accepted certificate type nor "PRIVATE KEY", and
// must not appear in the filtered output.
func TestFilterCertificatePEMDropsNonPKCS8PrivateKeyTypes(t *testing.T) {
	der := genCertDER(t)
	certSrc := "-----BEGIN CERTIFICATE-----\n" + rewrap(der, 64) + "\n-----END CERTIFICATE-----\n"

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	var ecKeyBuf bytes.Buffer
	if err := pem.Encode(&ecKeyBuf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: ecDER}); err != nil {
		t.Fatal(err)
	}

	combined := append(append([]byte{}, ecKeyBuf.Bytes()...), []byte(certSrc)...)
	got := certs.FilterCertificatePEM(combined)

	if bytes.Contains(got, []byte("EC PRIVATE KEY")) {
		t.Fatalf("filtered output carried an EC PRIVATE KEY block: %q", got)
	}
	if string(got) != certSrc {
		t.Errorf("filtered output = %q, want only the certificate block %q", got, certSrc)
	}
}

// TestFilterCertificatePEMDropsBlockSkippedBeforeAndBetween pins item 1: a
// block pem.Decode skips (mismatched END label) must not be folded into the
// certificate that follows it, whether it precedes the first certificate or
// sits between two.
func TestFilterCertificatePEMDropsBlockSkippedBeforeAndBetween(t *testing.T) {
	der1 := genCertDER(t)
	der2 := genCertDER(t)
	cert1 := "-----BEGIN CERTIFICATE-----\n" + rewrap(der1, 64) + "\n-----END CERTIFICATE-----\n"
	cert2 := "-----BEGIN CERTIFICATE-----\n" + rewrap(der2, 64) + "\n-----END CERTIFICATE-----\n"

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	var keyBuf bytes.Buffer
	if err := pem.Encode(&keyBuf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatal(err)
	}
	skipped := bytes.Replace(keyBuf.Bytes(), []byte("-----END EC PRIVATE KEY-----"), []byte("-----END WRONG LABEL-----"), 1)

	tests := []struct {
		name string
		file []byte
		want string
	}{
		{
			name: "skipped block precedes the only certificate",
			file: append(append([]byte{}, skipped...), []byte(cert1)...),
			want: cert1,
		},
		{
			name: "skipped block sits between two certificates",
			file: append(append(append([]byte{}, []byte(cert1)...), skipped...), []byte(cert2)...),
			want: cert1 + cert2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := certs.FilterCertificatePEM(tt.file)
			if string(got) != tt.want {
				t.Errorf("filtered output = %q, want %q", got, tt.want)
			}
			if bytes.Contains(got, []byte("EC PRIVATE KEY")) || bytes.Contains(got, []byte("WRONG LABEL")) {
				t.Errorf("filtered output leaked the skipped block: %q", got)
			}
		})
	}
}

// TestFilterCertificatePEMAcceptsLegacyX509CertificateLabel pins item 3:
// LoadCA does not check block.Type, so a CA file carrying the legacy OpenSSL
// "X509 CERTIFICATE" label loads and signs correctly. The filter must not
// treat that label as non-certificate content.
func TestFilterCertificatePEMAcceptsLegacyX509CertificateLabel(t *testing.T) {
	der := genCertDER(t)
	src := "-----BEGIN X509 CERTIFICATE-----\n" + rewrap(der, 64) + "\n-----END X509 CERTIFICATE-----\n"

	got := certs.FilterCertificatePEM([]byte(src))
	if string(got) != src {
		t.Errorf("filtered output = %q, want the legacy-labeled block unchanged %q", got, src)
	}
}

// TestFilterCertificatePEMNoCertificateBlockIsEmpty documents the filter's
// own contract for item 2: a file with no certificate block returns an
// empty slice. HandleGetCA (internal/handler) is responsible for treating
// that as a refusal rather than a 200 success.
func TestFilterCertificatePEMNoCertificateBlockIsEmpty(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	var keyBuf bytes.Buffer
	if err := pem.Encode(&keyBuf, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatal(err)
	}

	got := certs.FilterCertificatePEM(keyBuf.Bytes())
	if len(got) != 0 {
		t.Errorf("filtered output = %q, want empty for a key-only file", got)
	}
}

func TestLoadCAPair_MatchedPair(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Matched CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certFile := writeFile(t, dir, "ca.crt", certPEM)
	keyFile := writeFile(t, dir, "ca.key", keyPEM)

	cert, gotPEM, key, certErr, keyErr := certs.LoadCAPair(certFile, keyFile)
	if cert == nil {
		t.Fatal("cert = nil, want the parsed CA certificate")
	}
	if key == nil {
		t.Fatal("key = nil, want the parsed CA key for a matched pair")
	}
	if certErr != nil {
		t.Errorf("certErr = %v, want nil for a matched pair", certErr)
	}
	if keyErr != nil {
		t.Errorf("keyErr = %v, want nil for a matched pair", keyErr)
	}
	if string(gotPEM) != string(certPEM) {
		t.Error("certPEM does not equal the bytes on disk")
	}
}

// TestLoadCAPair_CertOnlyNoKeyFile is the fix for MEDIUM 1: a deployment
// that removed the CA key file still gets the certificate. Control:
// TestLoadCAPair_MatchedPair proves the same function returns a non-nil key
// when one is present, so this is not vacuously passing on an always-nil key.
func TestLoadCAPair_CertOnlyNoKeyFile(t *testing.T) {
	dir := t.TempDir()
	certPEM, _, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Keyless CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	certFile := writeFile(t, dir, "ca.crt", certPEM)
	missingKeyFile := filepath.Join(dir, "does-not-exist.key")

	cert, _, key, certErr, keyErr := certs.LoadCAPair(certFile, missingKeyFile)
	if cert == nil {
		t.Fatal("cert = nil, want the certificate to load even though the key file is absent")
	}
	if key != nil {
		t.Error("key != nil, want nil when the key file does not exist")
	}
	if certErr != nil {
		t.Errorf("certErr = %v, want nil: the certificate loaded, so nothing about it failed", certErr)
	}
	if keyErr == nil {
		t.Error("keyErr = nil, want a non-nil error naming the missing key")
	}
}

// TestLoadCAPair_MismatchedPair is the reproduction and fix for MEDIUM 2:
// a certificate rotated under SEP2_SERVING_CA/SEP2_DEVICE_CA while the old
// shared key stays in place must not load as a usable pair.
func TestLoadCAPair_MismatchedPair(t *testing.T) {
	dir := t.TempDir()
	newCertPEM, _, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 New CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(new): %v", err)
	}
	_, oldKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 Old CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA(old): %v", err)
	}
	certFile := writeFile(t, dir, "ca.crt", newCertPEM)
	keyFile := writeFile(t, dir, "ca.key", oldKeyPEM)

	cert, _, key, certErr, keyErr := certs.LoadCAPair(certFile, keyFile)
	if cert == nil {
		t.Fatal("cert = nil, want the new certificate to still load")
	}
	if key != nil {
		t.Error("key != nil, want nil for a certificate/key pair from two different CAs")
	}
	if certErr != nil {
		t.Errorf("certErr = %v, want nil: the certificate itself parsed fine", certErr)
	}
	if keyErr == nil {
		t.Error("keyErr = nil, want a non-nil error naming the mismatch")
	}
}

// TestLoadCAPair_CertMissing is also the reproduction for #638 fix round 3
// item 6: a caller branching on keyErr alone must not report a missing
// certificate as a key problem. Before the fix, the missing-certificate
// error was returned AS keyErr with certErr never existing, so the two
// causes were indistinguishable from the caller's side; this asserts
// certErr carries the failure and keyErr does not, and that certErr names
// the certificate rather than the key.
func TestLoadCAPair_CertMissing(t *testing.T) {
	dir := t.TempDir()
	_, keyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "638 CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	keyFile := writeFile(t, dir, "ca.key", keyPEM)
	missingCertFile := filepath.Join(dir, "does-not-exist.crt")

	cert, certPEM, key, certErr, keyErr := certs.LoadCAPair(missingCertFile, keyFile)
	if cert != nil {
		t.Error("cert != nil, want nil when the certificate file does not exist")
	}
	if certPEM != nil {
		t.Error("certPEM != nil, want nil alongside a nil cert")
	}
	if key != nil {
		t.Error("key != nil, want nil when the certificate itself never loaded")
	}
	if certErr == nil {
		t.Fatal("certErr = nil, want a non-nil error naming the missing certificate")
	}
	if !strings.Contains(certErr.Error(), "CA cert") {
		t.Errorf("certErr = %v, want it to name the certificate, not the key", certErr)
	}
	if keyErr != nil {
		t.Errorf("keyErr = %v, want nil: the key was never reached, so it must not carry the certificate's failure", keyErr)
	}
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}
