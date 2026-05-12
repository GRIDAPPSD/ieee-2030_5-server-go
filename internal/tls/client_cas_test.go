package tls_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	septls "github.com/GRIDAPPSD/ieee-2030_5-go/internal/tls"
)

// TestLoadClientCAs covers the multi-root ClientCA loader the IEEE-068
// Enphase runtime profile depends on. Each subtest is isolated under
// t.TempDir so concurrent runs cannot collide on disk.
func TestLoadClientCAs(t *testing.T) {
	t.Parallel()

	t.Run("one root loads", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		caPath := writeSelfSignedCA(t, dir, "primary")

		pool, err := septls.LoadClientCAs(caPath, nil)
		if err != nil {
			t.Fatalf("LoadClientCAs: %v", err)
		}
		assertPoolContains(t, pool, "primary")
	})

	t.Run("two roots load additively", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		primary := writeSelfSignedCA(t, dir, "primary")
		extra := writeSelfSignedCA(t, dir, "extra")

		pool, err := septls.LoadClientCAs(primary, []string{extra})
		if err != nil {
			t.Fatalf("LoadClientCAs: %v", err)
		}
		assertPoolContains(t, pool, "primary")
		assertPoolContains(t, pool, "extra")
	})

	t.Run("three roots load additively", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		primary := writeSelfSignedCA(t, dir, "primary")
		extraA := writeSelfSignedCA(t, dir, "extra-a")
		extraB := writeSelfSignedCA(t, dir, "extra-b")

		pool, err := septls.LoadClientCAs(primary, []string{extraA, extraB})
		if err != nil {
			t.Fatalf("LoadClientCAs: %v", err)
		}
		assertPoolContains(t, pool, "primary")
		assertPoolContains(t, pool, "extra-a")
		assertPoolContains(t, pool, "extra-b")
	})

	t.Run("bad extra path returns wrapped error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		primary := writeSelfSignedCA(t, dir, "primary")

		_, err := septls.LoadClientCAs(primary, []string{filepath.Join(dir, "does-not-exist.pem")})
		if err == nil {
			t.Fatal("expected error for missing extra CA, got nil")
		}
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) {
			t.Errorf("error chain does not include *os.PathError: %v", err)
		}
		if !strings.Contains(err.Error(), "does-not-exist.pem") {
			t.Errorf("error %q does not cite the offending path", err)
		}
	})

	t.Run("bad primary path returns wrapped error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()

		_, err := septls.LoadClientCAs(filepath.Join(dir, "missing.pem"), nil)
		if err == nil {
			t.Fatal("expected error for missing primary CA, got nil")
		}
		if !strings.Contains(err.Error(), "missing.pem") {
			t.Errorf("error %q does not cite the offending path", err)
		}
	})

	t.Run("empty primary path errors", func(t *testing.T) {
		t.Parallel()
		_, err := septls.LoadClientCAs("", nil)
		if err == nil {
			t.Fatal("expected error for empty primary CA, got nil")
		}
	})

	t.Run("nil extras is a no-op", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		primary := writeSelfSignedCA(t, dir, "primary")

		pool, err := septls.LoadClientCAs(primary, nil)
		if err != nil {
			t.Fatalf("LoadClientCAs: %v", err)
		}
		assertPoolContains(t, pool, "primary")
	})

	t.Run("empty slice is a no-op", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		primary := writeSelfSignedCA(t, dir, "primary")

		pool, err := septls.LoadClientCAs(primary, []string{})
		if err != nil {
			t.Fatalf("LoadClientCAs: %v", err)
		}
		assertPoolContains(t, pool, "primary")
	})

	t.Run("empty string entries are tolerated", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		primary := writeSelfSignedCA(t, dir, "primary")
		extra := writeSelfSignedCA(t, dir, "extra")

		pool, err := septls.LoadClientCAs(primary, []string{"", extra, ""})
		if err != nil {
			t.Fatalf("LoadClientCAs with empty entries: %v", err)
		}
		assertPoolContains(t, pool, "primary")
		assertPoolContains(t, pool, "extra")
	})

	t.Run("junk PEM returns parse error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		primary := writeSelfSignedCA(t, dir, "primary")
		junk := filepath.Join(dir, "junk.pem")
		if err := os.WriteFile(junk, []byte("not a pem file\n"), 0o600); err != nil {
			t.Fatalf("seed junk file: %v", err)
		}

		_, err := septls.LoadClientCAs(primary, []string{junk})
		if err == nil {
			t.Fatal("expected error for junk PEM, got nil")
		}
		if !strings.Contains(err.Error(), "junk.pem") {
			t.Errorf("error %q does not cite the offending path", err)
		}
	})
}

// writeSelfSignedCA writes a fresh self-signed ECDSA P-256 root CA to a
// PEM file under dir and returns its path. The CommonName is set to
// label so tests can assert pool membership by subject.
func writeSelfSignedCA(t *testing.T, dir, label string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key (%s): %v", label, err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: label},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert (%s): %v", label, err)
	}
	path := filepath.Join(dir, label+"-ca.pem")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encode pem (%s): %v", label, err)
	}
	return path
}

// assertPoolContains decodes the named CA subject and checks that the
// pool's Subjects() set contains it. Uses crypto/x509's exposed Subjects
// accessor — preferred to peeking at internal state.
func assertPoolContains(t *testing.T, pool *x509.CertPool, cn string) {
	t.Helper()
	for _, raw := range pool.Subjects() { //nolint:staticcheck // SA1019: stable across Go 1.21+ for our use
		var name pkix.RDNSequence
		if _, err := asn1.Unmarshal(raw, &name); err != nil {
			continue
		}
		for _, rdn := range name {
			for _, atv := range rdn {
				if s, ok := atv.Value.(string); ok && s == cn {
					return
				}
			}
		}
	}
	t.Errorf("pool does not contain CA with CN=%q (have %d subjects)", cn, len(pool.Subjects()))
}
