package server

// #657: adminClientCAPool's refusal and load-failure branches had no test
// (test coverage lane, PR 657), and a non-CA anchor loaded silently because
// AppendCertsFromPEM never inspects IsCA (error-handling lane, MEDIUM-2).
// These tests drive the unexported resolver directly, one branch at a time,
// against the shapes the design settled: system sentinel, unconfigured,
// unloadable, and loadable-but-not-a-CA each split on whether the operator
// set SEP2_ADMIN_CLIENT_CA explicitly.

import (
	"crypto/x509"
	"encoding/pem"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// writeNonCALeaf mints a leaf certificate (IsCA=false) signed by a
// throwaway root and writes only the leaf PEM to path, reproducing "loads
// but holds nothing usable as an anchor": AppendCertsFromPEM parses it
// without error since it never inspects IsCA.
func writeNonCALeaf(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "657 pool test root", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM: %v", err)
	}
	leafPEM, _, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1"},
		CommonName: "657 pool test leaf",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}
	if err := os.WriteFile(path, leafPEM, 0o600); err != nil {
		t.Fatalf("write non-CA leaf: %v", err)
	}
	leaf, err := certs.ParseCertificatePEM(leafPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(leaf): %v", err)
	}
	return leaf
}

// writeCAFile mints n independent, self-signed root CAs and writes their
// concatenated certificate PEM to path, returning all n in file order.
func writeCAFile(t *testing.T, path string, n int) []*x509.Certificate {
	t.Helper()
	var buf []byte
	var cas []*x509.Certificate
	for i := 0; i < n; i++ {
		certPEM, _, err := certs.GenerateCA(certs.CAOptions{CommonName: "657 pool test CA", ValidYears: 1})
		if err != nil {
			t.Fatalf("GenerateCA %d: %v", i, err)
		}
		buf = append(buf, certPEM...)
		cert, err := certs.ParseCertificatePEM(certPEM)
		if err != nil {
			t.Fatalf("ParseCertificatePEM %d: %v", i, err)
		}
		cas = append(cas, cert)
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}
	return cas
}

// writeCAWithMalformedBlock writes a valid, loadable CA certificate
// followed by a syntactically valid PEM CERTIFICATE block whose body is not
// valid DER, reproducing the #657 round 2 security lane's MEDIUM-1 shape:
// one good CA and one bad block in a single anchor file.
// x509.CertPool.AppendCertsFromPEM skips a block it cannot parse rather
// than failing the whole file; parseAdminClientCAPool must do the same.
func writeCAWithMalformedBlock(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	certPEM, _, err := certs.GenerateCA(certs.CAOptions{CommonName: "657 round 2 malformed-block CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM: %v", err)
	}
	malformed := []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")
	buf := append(append([]byte{}, certPEM...), malformed...)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write mixed anchor: %v", err)
	}
	return caCert
}

// writeHeaderedCAWithNonCALeaf writes a CA certificate re-encoded with a
// PEM header (a shape AppendCertsFromPEM skips outright, per its own
// len(block.Headers) != 0 check) followed by a non-CA leaf, reproducing the
// #657 round 2 security lane's MEDIUM-2 shape: the file's only CA-flagged
// block never reaches the pool, so nothing in it should count as a CA.
func writeHeaderedCAWithNonCALeaf(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{CommonName: "657 round 2 headered CA", ValidYears: 1})
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM: %v", err)
	}
	block, _ := pem.Decode(caCertPEM)
	if block == nil {
		t.Fatal("decode generated CA PEM")
	}
	headered := pem.EncodeToMemory(&pem.Block{
		Type:    "CERTIFICATE",
		Headers: map[string]string{"X-657-Round2-Test": "value"},
		Bytes:   block.Bytes,
	})
	leafPEM, _, err := certs.GenerateServerCert(caCert, caKey, certs.ServerCertOptions{
		Hosts:      []string{"127.0.0.1"},
		CommonName: "657 round 2 headered-anchor leaf",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("GenerateServerCert: %v", err)
	}
	buf := append(append([]byte{}, headered...), leafPEM...)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatalf("write headered anchor: %v", err)
	}
	return caCert
}

func TestAdminClientCAPoolSystemSentinel(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{AdminClientCA: config.AdminClientCASystemRoots}

	pool, desc, err := adminClientCAPool(cfg)
	if err != nil {
		t.Fatalf("adminClientCAPool: unexpected error: %v", err)
	}
	if pool != nil {
		t.Errorf("pool = %v, want nil (buildAdminTLSConfig leaves ClientCAs nil for the host-root fallback)", pool)
	}
	if want := "host root trust store (SEP2_ADMIN_CLIENT_CA=system)"; desc != want {
		t.Errorf("desc = %q, want %q", desc, want)
	}
}

// TestAdminClientCAPoolEmptyAnchorDegrades is design shape 1: no CA path
// configured anywhere. Red before the fix, where this branch returned a
// nil pool and an error naming remediation that main.go can never trigger
// (envPathOrCertDir always resolves a path).
func TestAdminClientCAPoolEmptyAnchorDegrades(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}

	pool, desc, err := adminClientCAPool(cfg)
	if err != nil {
		t.Fatalf("adminClientCAPool: unexpected error: %v", err)
	}
	if pool == nil {
		t.Fatal("pool = nil, want a non-nil empty pool (#418: nil reopens the host-root fallback)")
	}
	if !pool.Equal(x509.NewCertPool()) {
		t.Error("pool is not empty, want an empty pool (empty pool denies everything)")
	}
	if want := "none configured; operator certificate sign-in disabled"; desc != want {
		t.Errorf("desc = %q, want %q", desc, want)
	}
}

// TestAdminClientCAPoolDefaultedLoadFailureDegrades is design shape 2's
// default branch: the anchor came from EffectiveServingCA (not set
// explicitly) and the file does not exist. Red before the fix, where
// server.Run refused to start entirely for this shape (#657 security and
// error-handling lanes' HIGH-1).
func TestAdminClientCAPoolDefaultedLoadFailureDegrades(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist.crt")
	cfg := &config.Config{CAFile: missing}

	pool, desc, err := adminClientCAPool(cfg)
	if err != nil {
		t.Fatalf("adminClientCAPool: unexpected error: %v", err)
	}
	if pool == nil || !pool.Equal(x509.NewCertPool()) {
		t.Fatalf("pool = %v, want a non-nil empty pool", pool)
	}
	if !strings.Contains(desc, "NOT LOADED") || !strings.Contains(desc, missing) {
		t.Errorf("desc = %q, want it to contain %q and the path %q", desc, "NOT LOADED", missing)
	}
}

// TestAdminClientCAPoolExplicitLoadFailureRefuses is design shape 2's
// explicit branch. Red before the fix on the remedy text alone: the old
// code returned "load %s: %w" with no mention of SEP2_ADMIN_CLIENT_CA or
// the "system" escape hatch.
func TestAdminClientCAPoolExplicitLoadFailureRefuses(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist.crt")
	cfg := &config.Config{AdminClientCA: missing}

	pool, desc, err := adminClientCAPool(cfg)
	if err == nil {
		t.Fatalf("adminClientCAPool: want an error, got pool=%v desc=%q", pool, desc)
	}
	if pool != nil {
		t.Errorf("pool = %v, want nil on refusal", pool)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %v, want it to name the path %q", err, missing)
	}
	if !strings.Contains(err.Error(), "SEP2_ADMIN_CLIENT_CA") {
		t.Errorf("err = %v, want it to name SEP2_ADMIN_CLIENT_CA (the remedy)", err)
	}
}

// TestAdminClientCAPoolDefaultedNonCAAnchorDegrades is design shape 3's
// default branch. Red before the fix: AppendCertsFromPEM never checks
// IsCA, so the old code loaded the leaf as if it were a working anchor and
// returned its own path as the description with no error.
func TestAdminClientCAPoolDefaultedNonCAAnchorDegrades(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "leaf-only.crt")
	writeNonCALeaf(t, path)
	cfg := &config.Config{CAFile: path}

	pool, desc, err := adminClientCAPool(cfg)
	if err != nil {
		t.Fatalf("adminClientCAPool: unexpected error: %v", err)
	}
	if pool == nil || !pool.Equal(x509.NewCertPool()) {
		t.Fatalf("pool = %v, want a non-nil EMPTY pool: a leaf-only file must not verify as an anchor", pool)
	}
	if want := "no CA certificate in " + path; desc != want {
		t.Errorf("desc = %q, want %q", desc, want)
	}
}

// TestAdminClientCAPoolExplicitNonCAAnchorRefuses is design shape 3's
// explicit branch, issue #624 done-condition 3's counterpart for a bad
// file rather than a missing one. Red before the fix: no error at all.
func TestAdminClientCAPoolExplicitNonCAAnchorRefuses(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "leaf-only.crt")
	writeNonCALeaf(t, path)
	cfg := &config.Config{AdminClientCA: path}

	pool, desc, err := adminClientCAPool(cfg)
	if err == nil {
		t.Fatalf("adminClientCAPool: want an error, got pool=%v desc=%q", pool, desc)
	}
	if pool != nil {
		t.Errorf("pool = %v, want nil on refusal", pool)
	}
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "no CA certificate") {
		t.Errorf("err = %v, want it to name the path %q and \"no CA certificate\"", err, path)
	}
	if !strings.Contains(err.Error(), "SEP2_ADMIN_CLIENT_CA") {
		t.Errorf("err = %v, want it to name SEP2_ADMIN_CLIENT_CA (the remedy)", err)
	}
}

// TestAdminClientCAPoolSingleCADescriptionIncludesSubjectAndFingerprint is
// item 4: the startup line must distinguish a usable anchor from a
// leaf-only one, the way it already does for the serving/device CA roles.
func TestAdminClientCAPoolSingleCADescriptionIncludesSubjectAndFingerprint(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "one-ca.crt")
	cas := writeCAFile(t, path, 1)
	caCert := cas[0]
	cfg := &config.Config{CAFile: path}

	pool, desc, err := adminClientCAPool(cfg)
	if err != nil {
		t.Fatalf("adminClientCAPool: unexpected error: %v", err)
	}
	if pool == nil {
		t.Fatal("pool = nil, want the loaded pool")
	}
	if _, err := caCert.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
		t.Errorf("the generated CA does not verify against the returned pool: %v", err)
	}
	wantSubject, wantFingerprint := caRoleInfo(caCert)
	if !strings.Contains(desc, "(1 CA; "+wantSubject+"; "+wantFingerprint+")") {
		t.Errorf("desc = %q, want it to contain the (1 CA; subject; fingerprint) suffix with subject %q and fingerprint %q", desc, wantSubject, wantFingerprint)
	}
}

// TestAdminClientCAPoolMultiCADescriptionDropsSubjectAndFingerprint pins
// the design's "dropping subject and fingerprint above one" rule: with two
// CAs in the file neither identifies the operator's cert alone, so the
// banner names the count and nothing else.
func TestAdminClientCAPoolMultiCADescriptionDropsSubjectAndFingerprint(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "two-ca.crt")
	cas := writeCAFile(t, path, 2)
	cfg := &config.Config{CAFile: path}

	pool, desc, err := adminClientCAPool(cfg)
	if err != nil {
		t.Fatalf("adminClientCAPool: unexpected error: %v", err)
	}
	if pool == nil {
		t.Fatal("pool = nil, want the loaded pool")
	}
	for i, ca := range cas {
		if _, err := ca.Verify(x509.VerifyOptions{Roots: pool}); err != nil {
			t.Errorf("generated CA %d does not verify against the returned pool: %v", i, err)
		}
	}
	if want := path + " (2 CA)"; desc != want {
		t.Errorf("desc = %q, want %q (no subject/fingerprint above one CA)", desc, want)
	}
}

// TestAdminClientCAPoolMultiCALogsEachSubjectAndFingerprint is #657 round
// 2's LOW-5: the settled design pins the one-line banner to dropping
// subject and fingerprint above one CA, so above one CA the full detail
// goes to the startup log instead, the way it does for every degraded
// shape's own log.Printf. Not run with t.Parallel(): it redirects the
// shared stdlib log.Writer(), and Go holds every t.Parallel() test in this
// package until every non-parallel one (this one included) has finished,
// so no concurrent adminClientCAPool call can write into this buffer.
func TestAdminClientCAPoolMultiCALogsEachSubjectAndFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "two-ca.crt")
	cas := writeCAFile(t, path, 2)
	cfg := &config.Config{CAFile: path}

	var buf strings.Builder
	prevOut := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	if _, _, err := adminClientCAPool(cfg); err != nil {
		t.Fatalf("adminClientCAPool: unexpected error: %v", err)
	}

	got := buf.String()
	for i, ca := range cas {
		subject, fingerprint := caRoleInfo(ca)
		if !strings.Contains(got, subject) || !strings.Contains(got, fingerprint) {
			t.Errorf("CA %d: log output = %s, want it to name subject %q and fingerprint %q", i, got, subject, fingerprint)
		}
	}
}

// TestAdminClientCAPoolDefaultedMalformedBlockStillLoadsTheGoodCA is #657
// round 2's MEDIUM-1: a defaulted anchor holding a valid CA and a malformed
// CERTIFICATE block used to refuse the whole boot on the malformed block
// alone, the same shape as HIGH-1 for a narrower input - a defaulted anchor
// is meant to degrade, never take the process down. Red before the fix:
// adminClientCAPool returned "<path>: parse certificate 1: x509: malformed
// certificate" with a nil pool. x509.CertPool.AppendCertsFromPEM already
// skips a block it cannot parse instead of failing the whole file; the fix
// makes the admin resolver agree, so the good CA still loads.
func TestAdminClientCAPoolDefaultedMalformedBlockStillLoadsTheGoodCA(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mixed.crt")
	caCert := writeCAWithMalformedBlock(t, path)
	cfg := &config.Config{CAFile: path}

	pool, desc, err := adminClientCAPool(cfg)
	if err != nil {
		t.Fatalf("adminClientCAPool: unexpected error: %v (want the good CA to load, the malformed block skipped)", err)
	}
	if _, verr := caCert.Verify(x509.VerifyOptions{Roots: pool}); verr != nil {
		t.Errorf("the good CA does not verify against the returned pool: %v", verr)
	}
	wantSubject, wantFingerprint := caRoleInfo(caCert)
	if !strings.Contains(desc, "(1 CA; "+wantSubject+"; "+wantFingerprint+")") {
		t.Errorf("desc = %q, want it to contain the (1 CA; subject; fingerprint) suffix", desc)
	}
}

// TestAdminClientCAPoolExplicitMalformedBlockStillLoadsTheGoodCA is the
// explicit-anchor counterpart: a malformed block sitting next to a good CA
// is not itself a provenance-split refusal condition (an unloadable file or
// zero usable CAs still is, per TestAdminClientCAPoolExplicitLoadFailureRefuses
// and TestAdminClientCAPoolExplicitNonCAAnchorRefuses), so an operator who
// names this exact file with SEP2_ADMIN_CLIENT_CA gets the same working
// anchor a defaulted server would, not a refusal.
func TestAdminClientCAPoolExplicitMalformedBlockStillLoadsTheGoodCA(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "mixed.crt")
	caCert := writeCAWithMalformedBlock(t, path)
	cfg := &config.Config{AdminClientCA: path}

	pool, _, err := adminClientCAPool(cfg)
	if err != nil {
		t.Fatalf("adminClientCAPool: unexpected error: %v", err)
	}
	if _, verr := caCert.Verify(x509.VerifyOptions{Roots: pool}); verr != nil {
		t.Errorf("the good CA does not verify against the returned pool: %v", verr)
	}
}

// TestAdminClientCAPoolDescriptionNeverClaimsACAThePoolDoesNotHave is #657
// round 2's MEDIUM-2: AppendCertsFromPEM skips a PEM-headered CERTIFICATE
// block, so it never reaches the pool, but round 1's description came from
// a second, more permissive parser (certs.ParseCertificateChainPEM, which
// accepts headers) that counted it anyway: "1 CA" over a pool that chains
// nothing. Red before the fix (desc contained "1 CA" while caCert.Verify
// against the returned pool failed). The fix counts CAs from the exact
// blocks the pool itself was built from, so a block the pool never held can
// never be counted.
func TestAdminClientCAPoolDescriptionNeverClaimsACAThePoolDoesNotHave(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "headered.crt")
	caCert := writeHeaderedCAWithNonCALeaf(t, path)
	cfg := &config.Config{CAFile: path}

	pool, desc, err := adminClientCAPool(cfg)
	if err != nil {
		t.Fatalf("adminClientCAPool: unexpected error: %v", err)
	}
	if _, verr := caCert.Verify(x509.VerifyOptions{Roots: pool}); verr == nil {
		t.Fatal("the headered CA verified against the pool; AppendCertsFromPEM should have skipped its headered block")
	}
	if want := "no CA certificate in " + path; desc != want {
		t.Errorf("desc = %q, want %q (the pool holds no usable CA, so the description must say so, not claim one)", desc, want)
	}
}

// TestBuildAdminTLSConfigDegradedAnchorHasNonNilClientCAs is design test 2:
// every degraded shape must build a tls.Config whose ClientCAs is non-nil,
// since only a nil pool reaches crypto/x509's host-root fallback (#418).
// Making it fail is one edit: adminClientCAPool returning nil instead of
// x509.NewCertPool() on any degraded branch.
func TestBuildAdminTLSConfigDegradedAnchorHasNonNilClientCAs(t *testing.T) {
	t.Parallel()
	missingLoad := filepath.Join(t.TempDir(), "does-not-exist.crt")
	nonCAPath := filepath.Join(t.TempDir(), "leaf-only.crt")
	writeNonCALeaf(t, nonCAPath)

	for _, tc := range []struct {
		name string
		cfg  *config.Config
	}{
		{"shape 1: unconfigured", &config.Config{}},
		{"shape 2: defaulted load failure", &config.Config{CAFile: missingLoad}},
		{"shape 3: defaulted non-CA anchor", &config.Config{CAFile: nonCAPath}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tlsCfg, _, err := buildAdminTLSConfig(tc.cfg)
			if err != nil {
				t.Fatalf("buildAdminTLSConfig: unexpected error: %v", err)
			}
			if tlsCfg.ClientCAs == nil {
				t.Error("ClientCAs = nil, want a non-nil empty pool (#418)")
			}
		})
	}
}
