// Tests for the `make run-sunspec` target (#911). The recipe is run for
// real with SERVER pointed at a stub that dumps its environment, and with
// `-o build -o certs` so no binary is built and no CA is generated.
package makerunsunspec_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// writeSyntheticPEM writes a throwaway self-signed certificate to path.
func writeSyntheticPEM(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "synthetic-" + filepath.Base(path)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
}

type run struct {
	out     string
	envDump string
	err     error
}

// hermeticEnv returns the process environment without any SEP2_* variable,
// so a developer's exported address cannot change what the recipe sees.
func hermeticEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "SEP2_") {
			env = append(env, kv)
		}
	}
	return env
}

// runMake runs `make run-sunspec` from the repo root with the given
// variable assignments and returns combined output and the stub's env dump.
func runMake(t *testing.T, assigns ...string) run {
	t.Helper()
	return runMakeEnv(t, nil, assigns...)
}

// runMakeEnv is runMake with extra environment variables, which reach make
// as exported variables rather than command-line assignments.
func runMakeEnv(t *testing.T, extraEnv []string, assigns ...string) run {
	t.Helper()
	root := repoRoot(t)
	dir := t.TempDir()
	envDump := filepath.Join(dir, "env.txt")
	stub := filepath.Join(dir, "stub.sh")
	script := "#!/bin/sh\nenv > \"" + envDump + "\"\n"
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, stub)
	if err != nil {
		t.Fatal(err)
	}
	args := append([]string{"-C", root, "-o", "build", "-o", "certs", "run-sunspec", "SERVER=" + rel, "CERT_DIR=/local-ca"}, assigns...)
	cmd := exec.Command("make", args...)
	cmd.Env = append(append(hermeticEnv(), "HOME="+dir), extraEnv...)
	out, err := cmd.CombinedOutput()
	dump, _ := os.ReadFile(envDump)
	return run{out: string(out), envDump: string(dump), err: err}
}

func TestRunSunspecPassesVariablesToServerEnv(t *testing.T) {
	d := t.TempDir()
	chain, key, cas, bundle := filepath.Join(d, "chain.pem"), filepath.Join(d, "key.pem"), filepath.Join(d, "cas.pem"), filepath.Join(d, "bundle.pem")
	for _, p := range []string{chain, key, cas, bundle} {
		writeSyntheticPEM(t, p)
	}
	r := runMake(t, "SUNSPEC_SERVER_CHAIN="+chain, "SUNSPEC_SERVER_KEY="+key,
		"SUNSPEC_CLIENT_CAS="+cas, "SUNSPEC_SERVER_CA_BUNDLE="+bundle)
	if r.err != nil {
		t.Fatalf("make failed: %v\n%s", r.err, r.out)
	}
	for _, want := range []string{
		"SEP2_CERT=" + chain,
		"SEP2_KEY=" + key,
		"SEP2_EXTRA_CLIENT_CAS=" + cas,
		"SEP2_CA=/local-ca/ca.crt",
		"SEP2_CA_KEY=/local-ca/ca.key",
		"SEP2_ADDR=:8443",
	} {
		if !strings.Contains(r.envDump, want+"\n") {
			t.Errorf("server env missing line %q; got:\n%s", want, r.envDump)
		}
	}
	for _, want := range []string{
		"# Server chain:  " + chain,
		"# Server key:    " + key,
		"# Trusted client CAs (SEP2_EXTRA_CLIENT_CAS): " + cas,
		"#   --ca   " + bundle,
	} {
		if !strings.Contains(r.out, want) {
			t.Errorf("banner missing %q; got:\n%s", want, r.out)
		}
	}
}

func TestRunSunspecAddrOverridesAndEmptyFallback(t *testing.T) {
	d := t.TempDir()
	pem := filepath.Join(d, "x.pem")
	writeSyntheticPEM(t, pem)
	sunspec := []string{"SUNSPEC_SERVER_CHAIN=" + pem, "SUNSPEC_SERVER_KEY=" + pem,
		"SUNSPEC_CLIENT_CAS=" + pem, "SUNSPEC_SERVER_CA_BUNDLE=" + pem}
	cases := []struct {
		name    string
		env     []string
		assigns []string
		want    string
	}{
		{"exported value", []string{"SEP2_ADDR=:9443"}, nil, "SEP2_ADDR=:9443"},
		{"command line value", nil, []string{"SEP2_ADDR=:9444"}, "SEP2_ADDR=:9444"},
		{"empty exported value falls back", []string{"SEP2_ADDR="}, nil, "SEP2_ADDR=:8443"},
		{"empty command line value falls back", nil, []string{"SEP2_ADDR="}, "SEP2_ADDR=:8443"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := runMakeEnv(t, c.env, append(sunspec, c.assigns...)...)
			if r.err != nil {
				t.Fatalf("make failed: %v\n%s", r.err, r.out)
			}
			if !strings.Contains(r.envDump, c.want+"\n") {
				t.Errorf("server env missing %q; got:\n%s", c.want, r.envDump)
			}
		})
	}
}

func TestRunSunspecRefusesUnsetOrMissingVariable(t *testing.T) {
	d := t.TempDir()
	good := filepath.Join(d, "good.pem")
	writeSyntheticPEM(t, good)
	all := map[string]string{
		"SUNSPEC_SERVER_CHAIN":     good,
		"SUNSPEC_SERVER_KEY":       good,
		"SUNSPEC_CLIENT_CAS":       good,
		"SUNSPEC_SERVER_CA_BUNDLE": good,
	}
	for name := range all {
		var unset, missing []string
		for k, v := range all {
			if k != name {
				unset = append(unset, k+"="+v)
			}
			if k == name {
				missing = append(missing, k+"="+filepath.Join(d, "absent.pem"))
			} else {
				missing = append(missing, k+"="+v)
			}
		}
		r := runMake(t, unset...)
		if r.err == nil || !strings.Contains(r.out, "ERROR: "+name+" is not set") {
			t.Errorf("%s unset: want refusal naming it; err=%v\n%s", name, r.err, r.out)
		}
		if r.envDump != "" {
			t.Errorf("%s unset: server stub ran", name)
		}
		r = runMake(t, missing...)
		if r.err == nil || !strings.Contains(r.out, "ERROR: "+name+"=") {
			t.Errorf("%s missing file: want refusal naming it; err=%v\n%s", name, r.err, r.out)
		}
	}
}

func TestMakefileHasNoSunspecDefaultOutsideRepo(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "SUNSPEC_") && strings.Contains(line, "?=") {
			t.Errorf("SunSpec variable has a default: %q", line)
		}
		if strings.Contains(line, "knowledge") {
			t.Errorf("Makefile references a Knowledge path: %q", line)
		}
	}
}
