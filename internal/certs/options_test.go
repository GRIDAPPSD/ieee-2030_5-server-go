package certs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultHWTypeOIDParses(t *testing.T) {
	oid, err := ParseOID(DefaultHWTypeOID)
	if err != nil {
		t.Fatalf("ParseOID(DefaultHWTypeOID): %v", err)
	}
	if got := oid.String(); got != "1.3.6.1.4.1.40732.99" {
		t.Errorf("default OID = %s, want 1.3.6.1.4.1.40732.99", got)
	}
}

// TestNewDeviceScriptLeavesTheDefaultToTheBinary pins that scripts/new-device.sh
// owns no hardware type default: it passes --hw-type only when HW_TYPE is set,
// so a stub binary sees no flag otherwise and the binary decides. The Makefile
// is not pinned here: it has no value to compare any more, and the
// SEP2_HW_TYPE hand-off for `make certs` is covered by running it.
func TestNewDeviceScriptLeavesTheDefaultToTheBinary(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	for _, f := range []string{"ca.crt", "ca.key"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	argsFile := filepath.Join(dir, "args")
	stub := filepath.Join(dir, "stub.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+argsFile+"\"\ntouch \""+dir+"/d.crt\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	run := func(hwType string) string {
		t.Helper()
		_ = os.Remove(filepath.Join(dir, "d.crt"))
		cmd := exec.Command(bash, "../../scripts/new-device.sh")
		cmd.Env = append(os.Environ(), "CERT_DIR="+dir, "SERVER_BIN="+stub, "DEVICE_NAME=d", "SERIAL=S1", "FORCE=1", "HW_TYPE="+hwType)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("new-device.sh (HW_TYPE=%q): %v\n%s", hwType, err, out)
		}
		b, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if got := run(""); strings.Contains(got, "--hw-type") {
		t.Errorf("HW_TYPE unset: args carry --hw-type:\n%s", got)
	}
	if got := run("1.3.6.1.4.1.55555.42"); !strings.Contains(got, "--hw-type\n1.3.6.1.4.1.55555.42\n") {
		t.Errorf("HW_TYPE set: args do not carry it:\n%s", got)
	}
}

func TestResolveDefaultHWType(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k != DefaultHWTypeEnv {
				t.Errorf("read env %q, want only %q", k, DefaultHWTypeEnv)
			}
			return v
		}
	}
	if got, err := ResolveDefaultHWType(env("")); err != nil || got != DefaultHWTypeOID {
		t.Errorf("unset: got %q, %v; want %q", got, err, DefaultHWTypeOID)
	}
	if got, err := ResolveDefaultHWType(env("1.3.6.1.4.1.55555.42")); err != nil || got != "1.3.6.1.4.1.55555.42" {
		t.Errorf("set: got %q, %v; want the env value", got, err)
	}
	if got, err := ResolveDefaultHWType(env("1.+3.6")); err != nil || got != "1.3.6" {
		t.Errorf("non-canonical: got %q, %v; want the canonical 1.3.6", got, err)
	}
	_, err := ResolveDefaultHWType(env("not-an-oid"))
	if err == nil || !strings.Contains(err.Error(), "SEP2_HW_TYPE") {
		t.Errorf("malformed: err = %v, want one naming SEP2_HW_TYPE", err)
	}
}
