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

// rewrap re-encodes base64 text at a fixed column width, distinct from
// Go's own pem.Encode (which always wraps at 64 columns, LF only). A test
// that used pem.Encode output as its only fixture could not tell a
// normalizing filter from a byte-copying one, since the two forms would
// happen to be identical.
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

// canonical is the encoding FilterCertificatePEM emits for der: the design
// record's "parse, then pem.EncodeToMemory" contract.
func canonical(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// TestFilterCertificatePEMNormalizesWrapping pins Question 1 of the #644
// design: the filter selects by parsing and emits the parser's own
// canonical encoding, so a fixture wrapped at 48 columns (which Go's own
// encoder never produces) comes back re-wrapped at 64, not preserved.
func TestFilterCertificatePEMNormalizesWrapping(t *testing.T) {
	der := genCertDER(t)
	src := "-----BEGIN CERTIFICATE-----\n" + rewrap(der, 48) + "\n-----END CERTIFICATE-----\n"
	want := canonical(der)

	got, dropped := certs.FilterCertificatePEM([]byte(src))
	if string(got) != want {
		t.Errorf("filtered output = %q, want the canonical re-encoding %q", got, want)
	}
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want none: the only block present is the certificate", dropped)
	}
	// Control: prove the assertion can fail. The 48-column source is not
	// byte-equal to the canonical form, so this fixture does distinguish a
	// normalizing filter from one that copies the stored bytes unchanged.
	if src == want {
		t.Fatal("control failed: the 48-column source is already byte-identical to the canonical form")
	}
}

// TestFilterCertificatePEMDropsNonCertificateBlocks proves an EC-labeled
// private key block is dropped from the filtered output, and reported as a
// dropped block by its own label. It does NOT kill the mutant that selects
// by block.Type instead of by parsing: the block's label ("EC PRIVATE KEY")
// is already not "CERTIFICATE", so a label-based selector drops it too.
// TestFilterCertificatePEMNormalizesLegacyX509CertificateLabel is what kills
// that mutant (#644 fix round 2, LOW 5: reproduced by changing the
// selection to `block.Type != "CERTIFICATE"` and observing this test stay
// green while that one goes red).
func TestFilterCertificatePEMDropsNonCertificateBlocks(t *testing.T) {
	der := genCertDER(t)
	certSrc := "-----BEGIN CERTIFICATE-----\n" + rewrap(der, 64) + "\n-----END CERTIFICATE-----\n"
	want := canonical(der)

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
	got, dropped := certs.FilterCertificatePEM(combined)

	if bytes.Contains(got, []byte("EC PRIVATE KEY")) {
		t.Fatalf("filtered output carried an EC PRIVATE KEY block: %q", got)
	}
	if string(got) != want {
		t.Errorf("filtered output = %q, want only the certificate block %q", got, want)
	}
	if len(dropped) != 1 || dropped[0] != "EC PRIVATE KEY" {
		t.Errorf("dropped = %v, want exactly one \"EC PRIVATE KEY\" entry", dropped)
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
	want1 := canonical(der1)
	want2 := canonical(der2)

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
			want: want1,
		},
		{
			name: "skipped block sits between two certificates",
			file: append(append(append([]byte{}, []byte(cert1)...), skipped...), []byte(cert2)...),
			want: want1 + want2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := certs.FilterCertificatePEM(tt.file)
			if string(got) != tt.want {
				t.Errorf("filtered output = %q, want %q", got, tt.want)
			}
			if bytes.Contains(got, []byte("EC PRIVATE KEY")) || bytes.Contains(got, []byte("WRONG LABEL")) {
				t.Errorf("filtered output leaked the skipped block: %q", got)
			}
		})
	}
}

// TestFilterCertificatePEMSharedEndBeginLineDropsPrivateKey pins the shape
// the #644 design measured: pem.Decode accepts a BEGIN at offset 0 of its
// current search window even when the byte before it is not a newline, so
// a file whose PRIVATE KEY block's END marker and the certificate's BEGIN
// marker share one line defeats an offset-based line-start guard. The
// parse-based filter has no offset to get wrong: it never returns a block
// that does not parse as a certificate.
func TestFilterCertificatePEMSharedEndBeginLineDropsPrivateKey(t *testing.T) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	certDER := genCertDER(t)

	shared := "-----BEGIN PRIVATE KEY-----\n" + rewrap(keyDER, 64) + "\n" +
		"-----END -----BEGIN CERTIFICATE-----\n" + rewrap(certDER, 64) + "\n" +
		"-----END CERTIFICATE-----\n"
	want := canonical(certDER)

	got, dropped := certs.FilterCertificatePEM([]byte(shared))
	if bytes.Contains(got, []byte("PRIVATE KEY")) {
		t.Fatalf("filtered output leaked the private key on the shared END/BEGIN line: %q", got)
	}
	if string(got) != want {
		t.Errorf("filtered output = %q, want only the certificate %q", got, want)
	}
	// pem.Decode itself never surfaces the private key as a *pem.Block on
	// this shape (its own END search skips straight to the CERTIFICATE
	// BEGIN embedded in the same line, which is the bug this fix closes),
	// so it never reaches our parse-and-drop step: dropped is empty, not a
	// PRIVATE KEY entry. Confirmed by inspection of the pem.Decode source
	// this brief cites, not asserted here as a positive claim about a block
	// we never receive.
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want none: pem.Decode itself never returns the private key as a block on this shape", dropped)
	}
}

// TestFilterCertificatePEMNormalizesLegacyX509CertificateLabel pins
// Question 3 of the #644 design: LoadCA does not check block.Type, so a CA
// file carrying the legacy OpenSSL "X509 CERTIFICATE" label loads and
// signs correctly. The filter accepts that label on input (the bytes parse
// as a certificate) and normalizes it to "CERTIFICATE" on output, since
// byte preservation is not owed to a label being accepted as an alias. This
// is also what kills the mutant that selects blocks by block.Type instead of
// by parsing (#644 fix round 2, LOW 5): a label-based selector rejects this
// legacy label outright, so want would never match a mutated got.
func TestFilterCertificatePEMNormalizesLegacyX509CertificateLabel(t *testing.T) {
	der := genCertDER(t)
	src := "-----BEGIN X509 CERTIFICATE-----\n" + rewrap(der, 64) + "\n-----END X509 CERTIFICATE-----\n"
	want := canonical(der)

	got, dropped := certs.FilterCertificatePEM([]byte(src))
	if string(got) != want {
		t.Errorf("filtered output = %q, want the normalized label %q", got, want)
	}
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want none: the legacy label parses as a certificate", dropped)
	}
}

// TestFilterCertificatePEMNoCertificateBlockIsEmpty documents the filter's
// own contract: a file with no block that parses as a certificate returns
// an empty slice and reports every block found as dropped, which is what
// HandleGetCA's invariant-violation log line names (#644 fix round 2,
// MEDIUM 2).
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

	got, dropped := certs.FilterCertificatePEM(keyBuf.Bytes())
	if len(got) != 0 {
		t.Errorf("filtered output = %q, want empty for a key-only file", got)
	}
	if len(dropped) != 1 || dropped[0] != "PRIVATE KEY" {
		t.Errorf("dropped = %v, want exactly one \"PRIVATE KEY\" entry", dropped)
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
