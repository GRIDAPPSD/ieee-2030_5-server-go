// This file resolves the SunSpec V1.2 test PKI and decides whether its absence
// is a skip or a failure. It mirrors test/conformance/wadl clause for clause:
// same per-variable env override, same absent-versus-misconfigured split, same
// required switch, so a contributor who has learned SEP2_WADL_REQUIRED already
// knows CSIP_SUNSPEC_REQUIRED.
package csip_test

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
		return false, fmt.Errorf("%s=%q is not a boolean: use 1 or 0", envRequired, raw)
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

	cert, key, roots, problems := resolveFixtures()
	switch {
	case len(problems) == 0:
		return cert, key, roots
	case !required:
		t.Skipf("CSIP fixtures not provisioned; see test/csip/README.md (set %s=1 to make this a failure): %s",
			envRequired, strings.Join(problems, "; "))
	default:
		t.Fatalf("%s is set but the SunSpec PKI is not usable: %s", envRequired, strings.Join(problems, "; "))
	}
	return "", "", ""
}

// resolveFixtures returns the three fixture paths plus one problem string per
// path that cannot be used. An empty problem list means all three are non-empty
// regular files. Env vars override the default paths, evaluated per variable.
//
// Empty and directory count as unusable rather than present: a reassembly step
// that produced a zero-byte file would otherwise satisfy a stat and arm the gate
// against nothing.
func resolveFixtures() (certPath, keyPath, rootsPath string, problems []string) {
	cert := fixturePath(envCert, "cert.pem")
	key := fixturePath(envKey, "key.pem")
	roots := fixturePath(envRoots, "roots.pem")

	for _, f := range []struct{ env, path string }{
		{envCert, cert},
		{envKey, key},
		{envRoots, roots},
	} {
		if problem := fixtureProblem(f.path); problem != "" {
			problems = append(problems, fmt.Sprintf("%s -> %s: %s", f.env, f.path, problem))
		}
	}
	return cert, key, roots, problems
}

func fixtureProblem(path string) string {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "no such file"
	case err != nil:
		return err.Error()
	case info.IsDir():
		return "is a directory, not a PEM file"
	case info.Size() == 0:
		return "is empty"
	}
	return ""
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

	// Loading the material, rather than only stat-ing it, is what makes PASS
	// mean usable: a cert and key from a half-rotated secret would otherwise
	// arm the gate and then fail deep in a handshake as if the server were at
	// fault. LoadX509KeyPair is what checks the two halves agree.
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load SunSpec cert and key: %v", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		t.Fatalf("parse SunSpec leaf: %v", err)
	}

	rootsPEM, err := os.ReadFile(rootsPath)
	if err != nil {
		t.Fatalf("read SunSpec roots: %v", err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(rootsPEM) {
		t.Fatalf("%s carries no PEM certificates", rootsPath)
	}

	t.Logf("CSIP fixture gate armed: cert=%s key=%s roots=%s, leaf notAfter=%s",
		certPath, keyPath, rootsPath, leaf.NotAfter.UTC().Format(time.RFC3339))
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

// TestResolveFixturesRejectsUnusableMaterial pins the three ways a configured
// path can be present but useless. Each one would otherwise arm the gate
// against nothing, which is worse than a skip because it reports a conformance
// result that was never measured.
func TestResolveFixturesRejectsUnusableMaterial(t *testing.T) {
	dir := t.TempDir()

	usable := filepath.Join(dir, "usable.pem")
	if err := os.WriteFile(usable, []byte("-----BEGIN CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatalf("write usable fixture: %v", err)
	}
	empty := filepath.Join(dir, "empty.pem")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatalf("write empty fixture: %v", err)
	}
	asDir := filepath.Join(dir, "adirectory")
	if err := os.Mkdir(asDir, 0o700); err != nil {
		t.Fatalf("make directory fixture: %v", err)
	}
	absent := filepath.Join(dir, "absent.pem")

	cases := []struct {
		name       string
		cert       string
		key        string
		roots      string
		wantEnv    string
		wantReason string
	}{
		{name: "absent cert", cert: absent, key: usable, roots: usable, wantEnv: envCert, wantReason: "no such file"},
		{name: "empty key", cert: usable, key: empty, roots: usable, wantEnv: envKey, wantReason: "is empty"},
		{name: "roots is a directory", cert: usable, key: usable, roots: asDir, wantEnv: envRoots, wantReason: "is a directory"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(envCert, tc.cert)
			t.Setenv(envKey, tc.key)
			t.Setenv(envRoots, tc.roots)

			gotCert, gotKey, gotRoots, problems := resolveFixtures()
			if gotCert != tc.cert || gotKey != tc.key || gotRoots != tc.roots {
				t.Errorf("resolveFixtures() paths = (%s, %s, %s), want (%s, %s, %s)",
					gotCert, gotKey, gotRoots, tc.cert, tc.key, tc.roots)
			}
			if len(problems) != 1 {
				t.Fatalf("resolveFixtures() problems = %v, want exactly one", problems)
			}
			if !strings.Contains(problems[0], tc.wantEnv) {
				t.Errorf("problem %q does not name %s, so a reader cannot tell which path to fix", problems[0], tc.wantEnv)
			}
			if !strings.Contains(problems[0], tc.wantReason) {
				t.Errorf("problem %q does not carry the reason %q", problems[0], tc.wantReason)
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
