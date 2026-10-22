package certs

import (
	"os"
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

func TestDefaultHWTypeOIDMatchesScripts(t *testing.T) {
	for _, f := range []string{"../../Makefile", "../../scripts/new-device.sh"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if !strings.Contains(string(b), DefaultHWTypeOID) {
			t.Errorf("%s does not carry the default hardware type %s", f, DefaultHWTypeOID)
		}
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
	_, err := ResolveDefaultHWType(env("not-an-oid"))
	if err == nil || !strings.Contains(err.Error(), "SEP2_HW_TYPE") {
		t.Errorf("malformed: err = %v, want one naming SEP2_HW_TYPE", err)
	}
}
