// This file resolves the SunSpec V1.2 test PKI and decides whether its absence
// is a skip or a failure. It mirrors test/conformance/wadl clause for clause:
// same per-variable env override, same absent-versus-misconfigured split, same
// required switch, so a contributor who has learned SEP2_WADL_REQUIRED already
// knows CSIP_SUNSPEC_REQUIRED.
package csip_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
)

// Fixture resolution: env first, then default to test/csip/fixtures/sunspec/.
const (
	envCert  = "CSIP_SUNSPEC_CERT"
	envKey   = "CSIP_SUNSPEC_KEY"
	envRoots = "CSIP_SUNSPEC_ROOTS"

	// envRequired converts an absent SunSpec PKI from a skip into a hard
	// failure. See sunspecRequired.
	envRequired = "CSIP_SUNSPEC_REQUIRED"
)

// sunspecRequired reports whether the caller has demanded that the SunSpec PKI
// be present, by setting envRequired to a truthy value.
//
// Unset and empty are the only values that disarm the gate, and an unparseable
// value is an error rather than a shrug: a run configured with
// CSIP_SUNSPEC_REQUIRED=ture must not disarm it. "Not configured" has to stay
// distinguishable from "configured wrong", because the second one silently
// reinstates the skip this gate exists to remove.
func sunspecRequired() (bool, error) {
	raw := strings.TrimSpace(os.Getenv(envRequired))
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s=%q is not a boolean; accepted values are 1, t, T, true, TRUE, True and their 0, f, F, false, FALSE, False counterparts: %w", envRequired, raw, err)
	}
	return v, nil
}

// mustResolveFixtures returns the SunSpec PKI paths. It SKIPS the calling test
// when the material is not provisioned, so a fresh clone runs the suite green,
// and FAILS when the material is required but unusable, or when the gate itself
// is misconfigured.
//
// envRequired is the switch CI flips once it supplies the material. Without it
// a deleted secret or a typo in one path variable would skip every
// SunSpec-backed procedure and still report green.
func mustResolveFixtures(t *testing.T) (certPath, keyPath, rootsPath string) {
	t.Helper()

	// Checked before the resolution result so a malformed envRequired is
	// reported even on a run where the material happens to be present.
	required, err := sunspecRequired()
	if err != nil {
		t.Fatalf("CSIP fixture gate configuration: %v", err)
	}

	cert, key, roots, issues := resolveFixtures()
	switch gateDecision(issues, required) {
	case verdictProceed:
		return cert, key, roots
	case verdictBroken:
		t.Fatalf("the SunSpec PKI is present but unusable: %s", joinIssues(issues))
	case verdictSkip:
		t.Skipf("CSIP fixtures not provisioned; see test/csip/README.md (set %s=1 to make this a failure): %s",
			envRequired, joinIssues(issues))
	case verdictAbsentButRequired:
		t.Fatalf("%s is set but the SunSpec PKI is not available: %s", envRequired, joinIssues(issues))
	}
	return "", "", ""
}

// gateVerdict is what the gate does about a set of issues. It is a named value
// rather than an inline switch so the branch structure can be asserted directly:
// the failure this gate exists to prevent is a branch that swallows a case.
type gateVerdict int

const (
	verdictProceed gateVerdict = iota
	verdictBroken
	verdictSkip
	verdictAbsentButRequired
)

// gateDecision mirrors MustLoadModel in test/conformance/wadl, including the
// clause order. verdictBroken comes BEFORE the required check on purpose: only
// absence may be skipped, so present-but-wrong material fails however the gate
// is configured. Collapsing those two is how misconfiguration reports green.
func gateDecision(issues []fixtureIssue, required bool) gateVerdict {
	switch {
	case len(issues) == 0:
		return verdictProceed
	case anyBroken(issues):
		return verdictBroken
	case !required:
		return verdictSkip
	default:
		return verdictAbsentButRequired
	}
}

// fixtureIssue is one reason a configured fixture path cannot be used. broken
// separates "present and wrong" from "not there at all", which is the only
// distinction the gate is allowed to treat differently: absence is a legitimate
// state for a fresh clone, wrongness never is.
type fixtureIssue struct {
	env    string
	path   string
	reason string
	broken bool
}

func (i fixtureIssue) String() string {
	return fmt.Sprintf("%s -> %s: %s", i.env, i.path, i.reason)
}

func joinIssues(issues []fixtureIssue) string {
	parts := make([]string, 0, len(issues))
	for _, i := range issues {
		parts = append(parts, i.String())
	}
	return strings.Join(parts, "; ")
}

func anyBroken(issues []fixtureIssue) bool {
	for _, i := range issues {
		if i.broken {
			return true
		}
	}
	return false
}

// resolveFixtures returns the three fixture paths plus one issue per path that
// cannot be used. No issues means all three are files carrying at least one
// complete PEM block of the kind that belongs there. Env vars override the
// default paths, evaluated per variable.
func resolveFixtures() (certPath, keyPath, rootsPath string, issues []fixtureIssue) {
	cert := fixturePath(envCert, "cert.pem")
	key := fixturePath(envKey, "key.pem")
	roots := fixturePath(envRoots, "roots.pem")

	for _, f := range []struct {
		env  string
		path string
		kind string
	}{
		{envCert, cert, blockCertificate},
		{envKey, key, blockPrivateKey},
		{envRoots, roots, blockCertificate},
	} {
		if issue, bad := fixtureIssueFor(f.env, f.path, f.kind); bad {
			issues = append(issues, issue)
		}
	}
	return cert, key, roots, issues
}

const (
	blockCertificate = "CERTIFICATE"
	blockPrivateKey  = "PRIVATE KEY"
)

// fixtureIssueFor classifies one path. Only a missing file is absent; every
// other fault is broken, including an empty file and a PEM header with no
// matching footer, because a truncated download and a half-written reassembly
// land exactly there and neither may be mistaken for "not provisioned".
func fixtureIssueFor(env, path, kind string) (fixtureIssue, bool) {
	issue := fixtureIssue{env: env, path: path, broken: true}

	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		issue.reason, issue.broken = "no such file", false
		return issue, true
	case err != nil:
		issue.reason = err.Error()
		return issue, true
	case info.IsDir():
		issue.reason = "is a directory, not a PEM file"
		return issue, true
	case info.Size() == 0:
		issue.reason = "is empty"
		return issue, true
	}

	// Read to confirm a complete PEM block. The bytes are never logged: a
	// reason string here would otherwise be able to carry key material.
	data, err := os.ReadFile(path)
	if err != nil {
		issue.reason = err.Error()
		return issue, true
	}
	if !hasPEMBlock(data, kind) {
		issue.reason = "carries no complete " + kind + " PEM block"
		return issue, true
	}
	return fixtureIssue{}, false
}

func hasPEMBlock(data []byte, kind string) bool {
	for rest := data; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return false
		}
		if block.Type == kind || strings.HasSuffix(block.Type, kind) {
			return true
		}
	}
}

func fixturePath(env, leaf string) string {
	if v := os.Getenv(env); v != "" {
		return v
	}
	return filepath.Join("fixtures", "sunspec", leaf)
}

// TestCSIPFixtureGateArmed is the canary that tells "the SunSpec-backed CSIP
// procedures ran" apart from "they were skipped", without parsing prose out of
// go test output. It is the single test name to look for: PASS means the
// external SunSpec V1.2 test PKI was located and loads, so those procedures
// really executed; SKIP means no test exercised that specific material.
//
// A SKIP here says nothing about mTLS coverage generally: TestDeviceHandshake
// runs unconditionally against the committed test-device PKI. What this canary
// scopes is the externally issued SunSpec material only.
//
// Under CSIP_SUNSPEC_REQUIRED an absent PKI fails here instead of skipping, so
// a run that is supposed to supply one cannot report green without it.
//
// This mirrors TestWADLGateArmed in test/conformance/wadl. Same purpose, same
// name shape, so one habit covers both artifacts.
func TestCSIPFixtureGateArmed(t *testing.T) {
	certPath, keyPath, rootsPath := mustResolveFixtures(t)

	leaf, err := checkSunSpecMaterial(certPath, keyPath, rootsPath)
	if err != nil {
		t.Fatalf("SunSpec material at %s is not usable: %v", certPath, err)
	}

	// The LFDI is logged, not asserted: pinning an expected identity here would
	// tie arming to one specific PKI. Logging it means a run armed against the
	// wrong material says so in the record a reader already has.
	t.Logf("CSIP fixture gate armed: cert=%s key=%s roots=%s, leaf LFDI=%s notAfter=%s",
		certPath, keyPath, rootsPath, sepTLS.LFDI(leaf), leaf.NotAfter.UTC().Format(time.RFC3339))
}

// checkSunSpecMaterial reports whether the material at the three paths is
// something a CSIP handshake could actually use: the key matches the cert, the
// leaf is unexpired, every certificate in the bundle parses, and the leaf
// verifies against that bundle.
//
// Verification goes through the server's own hook rather than x509.Verify
// because an IEEE 2030.5 6.11 leaf carries a critical HardwareModuleName SAN
// that stdlib x509 refuses outright. Using the hook means the canary accepts
// exactly the material the server would, so a PASS here is a statement about
// the handshake rather than about PEM syntax.
func checkSunSpecMaterial(certPath, keyPath, rootsPath string) (*x509.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("cert and key do not load as a pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse leaf: %w", err)
	}

	rootsPEM, err := os.ReadFile(rootsPath)
	if err != nil {
		return nil, fmt.Errorf("read roots: %w", err)
	}
	pool, count, err := parseCertPool(rootsPEM)
	if err != nil {
		return nil, fmt.Errorf("%s %w", rootsPath, err)
	}

	// Checked before verification only so the message names expiry directly;
	// Verify would reject an expired leaf anyway.
	if !leaf.NotAfter.After(time.Now()) {
		return nil, fmt.Errorf("leaf expired at %s", leaf.NotAfter.UTC().Format(time.RFC3339))
	}
	if err := sepTLS.VerifyPeerCertWithHardwareModuleSAN(pair.Certificate, pool); err != nil {
		return nil, fmt.Errorf("leaf does not verify against the %d certificate(s) in the bundle: %w", count, err)
	}
	return leaf, nil
}

// parseCertPool requires EVERY certificate block in a bundle to parse, where
// AppendCertsFromPEM reports success on one good certificate followed by
// garbage. A bundle whose second entry was truncated in transit would otherwise
// arm the gate with a trust anchor set nobody intended.
//
// Trailing non-PEM text is tolerated, because bundles in circulation carry
// human-readable subject lines; a trailing BEGIN marker is not, because that is
// what a block that failed to decode leaves behind.
func parseCertPool(pemBytes []byte) (*x509.CertPool, int, error) {
	pool := x509.NewCertPool()
	count := 0
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != blockCertificate {
			return nil, 0, fmt.Errorf("carries a %q PEM block where only %s belongs", block.Type, blockCertificate)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, 0, fmt.Errorf("certificate %d does not parse: %w", count+1, err)
		}
		pool.AddCert(cert)
		count++
	}
	if count == 0 {
		return nil, 0, errors.New("carries no PEM certificate")
	}
	if bytes.Contains(rest, []byte("-----BEGIN")) {
		return nil, 0, fmt.Errorf("carries a PEM block after certificate %d that does not decode", count)
	}
	return pool, count, nil
}

// TestSunSpecRequired covers the switch that keeps an absent SunSpec PKI from
// being silently satisfiable.
func TestSunSpecRequired(t *testing.T) {
	tests := []struct {
		raw     string
		want    bool
		wantErr bool
	}{
		{raw: "", want: false},
		{raw: "   ", want: false},
		{raw: "0", want: false},
		{raw: "false", want: false},
		{raw: "FALSE", want: false},
		{raw: "1", want: true},
		{raw: "true", want: true},
		{raw: "TRUE", want: true},
		{raw: " 1 ", want: true},
		{raw: "ture", wantErr: true},
		{raw: "yes", wantErr: true},
		{raw: "2", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("value %q", tc.raw), func(t *testing.T) {
			t.Setenv(envRequired, tc.raw)

			got, err := sunspecRequired()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("sunspecRequired() = %v, want an error for %q", got, tc.raw)
				}
				if got {
					t.Errorf("sunspecRequired() = true alongside an error; an unreadable value must not arm the gate")
				}
				// Wrapped rather than restated, so the message names the real
				// accepted set instead of a narrower guess at it.
				if !errors.Is(err, strconv.ErrSyntax) {
					t.Errorf("sunspecRequired() error = %v, want it to wrap strconv.ErrSyntax", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("sunspecRequired(): %v", err)
			}
			if got != tc.want {
				t.Errorf("sunspecRequired() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestGateDecision asserts every branch of the gate, including the one whose
// absence was the original defect: present-but-broken material must fail even
// with the gate disarmed.
func TestGateDecision(t *testing.T) {
	absent := []fixtureIssue{{env: envCert, reason: "no such file"}}
	broken := []fixtureIssue{{env: envCert, reason: "is empty", broken: true}}
	mixed := []fixtureIssue{
		{env: envCert, reason: "no such file"},
		{env: envKey, reason: "is empty", broken: true},
	}

	tests := []struct {
		name     string
		issues   []fixtureIssue
		required bool
		want     gateVerdict
	}{
		{name: "clean disarmed", issues: nil, required: false, want: verdictProceed},
		{name: "clean armed", issues: nil, required: true, want: verdictProceed},
		{name: "absent disarmed skips", issues: absent, required: false, want: verdictSkip},
		{name: "absent armed fails", issues: absent, required: true, want: verdictAbsentButRequired},
		{name: "broken disarmed still fails", issues: broken, required: false, want: verdictBroken},
		{name: "broken armed fails", issues: broken, required: true, want: verdictBroken},
		{name: "one broken among absent fails", issues: mixed, required: false, want: verdictBroken},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := gateDecision(tc.issues, tc.required); got != tc.want {
				t.Errorf("gateDecision() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestResolveFixturesClassifiesFaults pins which faults are absence and which
// are breakage, since only the first may be skipped. The header-only PEM case
// is the one that used to count as usable material.
func TestResolveFixturesClassifiesFaults(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, rootsPEM := mintSunSpecMaterial(t)

	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}
	goodCert := write("cert.pem", certPEM)
	goodKey := write("key.pem", keyPEM)
	goodRoots := write("roots.pem", rootsPEM)
	empty := write("empty.pem", nil)
	headerOnly := write("header.pem", []byte("-----BEGIN CERTIFICATE-----\n"))
	wrongKind := write("wrongkind.pem", keyPEM)
	asDir := filepath.Join(dir, "adirectory")
	if err := os.Mkdir(asDir, 0o700); err != nil {
		t.Fatalf("make directory fixture: %v", err)
	}
	absent := filepath.Join(dir, "absent.pem")

	tests := []struct {
		name       string
		cert       string
		key        string
		roots      string
		wantEnv    string
		wantReason string
		wantBroken bool
	}{
		{name: "absent cert is absence", cert: absent, key: goodKey, roots: goodRoots,
			wantEnv: envCert, wantReason: "no such file", wantBroken: false},
		{name: "empty key is breakage", cert: goodCert, key: empty, roots: goodRoots,
			wantEnv: envKey, wantReason: "is empty", wantBroken: true},
		{name: "directory is breakage", cert: goodCert, key: goodKey, roots: asDir,
			wantEnv: envRoots, wantReason: "is a directory", wantBroken: true},
		{name: "header-only PEM is breakage", cert: headerOnly, key: goodKey, roots: goodRoots,
			wantEnv: envCert, wantReason: "carries no complete CERTIFICATE PEM block", wantBroken: true},
		{name: "key where a certificate belongs is breakage", cert: wrongKind, key: goodKey, roots: goodRoots,
			wantEnv: envCert, wantReason: "carries no complete CERTIFICATE PEM block", wantBroken: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envCert, tc.cert)
			t.Setenv(envKey, tc.key)
			t.Setenv(envRoots, tc.roots)

			gotCert, gotKey, gotRoots, issues := resolveFixtures()
			if gotCert != tc.cert || gotKey != tc.key || gotRoots != tc.roots {
				t.Errorf("resolveFixtures() paths = (%s, %s, %s), want (%s, %s, %s)",
					gotCert, gotKey, gotRoots, tc.cert, tc.key, tc.roots)
			}
			if len(issues) != 1 {
				t.Fatalf("resolveFixtures() issues = %v, want exactly one", issues)
			}
			if issues[0].env != tc.wantEnv {
				t.Errorf("issue names %s, want %s, so a reader cannot tell which path to fix", issues[0].env, tc.wantEnv)
			}
			if !strings.Contains(issues[0].reason, tc.wantReason) {
				t.Errorf("issue reason %q does not carry %q", issues[0].reason, tc.wantReason)
			}
			if issues[0].broken != tc.wantBroken {
				t.Errorf("issue broken = %v, want %v; a broken fixture that reads as absence can be skipped",
					issues[0].broken, tc.wantBroken)
			}
			wantVerdict := verdictSkip
			if tc.wantBroken {
				wantVerdict = verdictBroken
			}
			if got := gateDecision(issues, false); got != wantVerdict {
				t.Errorf("disarmed gateDecision() = %d, want %d", got, wantVerdict)
			}
		})
	}
}

// TestGateArmedWithAbsentMaterialIsFatal is the contract case the tables above
// do not cover: with envRequired set and no material present, the gate must FAIL
// rather than skip. mustResolveFixtures calls t.Fatalf, so the two values it
// branches on are asserted here instead of calling it and aborting this test.
func TestGateArmedWithAbsentMaterialIsFatal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envCert, filepath.Join(dir, "cert.pem"))
	t.Setenv(envKey, filepath.Join(dir, "key.pem"))
	t.Setenv(envRoots, filepath.Join(dir, "roots.pem"))
	t.Setenv(envRequired, "1")

	required, err := sunspecRequired()
	if err != nil {
		t.Fatalf("sunspecRequired(): %v", err)
	}
	if !required {
		t.Fatalf("sunspecRequired() = false with %s=1", envRequired)
	}

	if _, _, _, problems := resolveFixtures(); len(problems) != 3 {
		t.Fatalf("resolveFixtures() problems = %v, want one per absent path", problems)
	}
}

// TestCheckSunSpecMaterial is the mutation guard on the canary's assertions.
// Each row is material that loads as PEM and would have armed the gate before
// the chain and expiry checks existed.
func TestCheckSunSpecMaterial(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM, rootsPEM := mintSunSpecMaterial(t)
	// A second, unrelated CA: its leaf is well-formed and chains to nothing in
	// the bundle above, which is the substituted-PKI case.
	otherCertPEM, otherKeyPEM, _ := mintSunSpecMaterial(t)
	expiredCertPEM, expiredKeyPEM := mintExpiredLeaf(t)

	write := func(name string, data []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return path
	}

	truncated := append(append([]byte{}, rootsPEM...), []byte("-----BEGIN CERTIFICATE-----\nnot base64\n")...)

	tests := []struct {
		name    string
		cert    string
		key     string
		roots   string
		wantErr string
	}{
		{name: "material that chains", cert: write("ok-cert.pem", certPEM),
			key: write("ok-key.pem", keyPEM), roots: write("ok-roots.pem", rootsPEM)},
		{name: "leaf from another CA", cert: write("other-cert.pem", otherCertPEM),
			key: write("other-key.pem", otherKeyPEM), roots: write("ok-roots2.pem", rootsPEM),
			wantErr: "does not verify"},
		{name: "expired leaf", cert: write("exp-cert.pem", expiredCertPEM),
			key: write("exp-key.pem", expiredKeyPEM), roots: write("exp-roots.pem", expiredCertPEM),
			wantErr: "leaf expired at"},
		{name: "key belongs to another cert", cert: write("mm-cert.pem", certPEM),
			key: write("mm-key.pem", otherKeyPEM), roots: write("ok-roots3.pem", rootsPEM),
			wantErr: "do not load as a pair"},
		{name: "bundle with an undecodable second entry", cert: write("tr-cert.pem", certPEM),
			key: write("tr-key.pem", keyPEM), roots: write("tr-roots.pem", truncated),
			wantErr: "does not decode"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			leaf, err := checkSunSpecMaterial(tc.cert, tc.key, tc.roots)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("checkSunSpecMaterial() = %v, want nil", err)
				}
				if leaf == nil {
					t.Fatal("checkSunSpecMaterial() returned no leaf on success")
				}
				return
			}
			if err == nil {
				t.Fatalf("checkSunSpecMaterial() = nil, want an error mentioning %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("checkSunSpecMaterial() = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// mintSunSpecMaterial returns a CSIP 6.11-shaped device leaf, its key, and the
// self-signed root that issued it, all ephemeral. Generated rather than
// committed: no certificate or key material belongs in this repository, and a
// generated chain also proves the critical HardwareModuleName SAN path, which a
// hand-rolled self-signed cert would not exercise.
func mintSunSpecMaterial(t *testing.T) (certPEM, keyPEM, rootsPEM []byte) {
	t.Helper()

	rootsPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName:   "CSIP gate test root",
		Organization: "gate test",
	})
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(rootsPEM)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("parse CA key: %v", err)
	}
	certPEM, keyPEM, err = certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "GATE-TEST-001",
	})
	if err != nil {
		t.Fatalf("generate device cert: %v", err)
	}
	return certPEM, keyPEM, rootsPEM
}

// mintExpiredLeaf returns a self-signed leaf whose validity ended yesterday.
// internal/certs deliberately cannot produce one, so this is built directly.
func mintExpiredLeaf(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "expired gate test leaf"},
		NotBefore:             time.Now().AddDate(0, 0, -2),
		NotAfter:              time.Now().AddDate(0, 0, -1),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create expired certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
