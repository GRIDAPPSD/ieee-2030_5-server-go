package certs_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

// rewrap re-encodes base64 text at a fixed column width, distinct from
// Go's own pem.Encode (which always wraps at 64 columns, LF only). A test
// that used pem.Encode output as its only fixture could pass under an
// implementation that re-encodes instead of copying source bytes, because
// the "re-encoded" and "source" forms would happen to be identical.
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
