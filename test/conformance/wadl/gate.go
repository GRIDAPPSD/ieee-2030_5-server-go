package wadl

import (
	"errors"
	"sync"
	"testing"
)

// This file is the test-facing gate. It mirrors internal/xsdgate/gate.go in
// the core module clause for clause, because the two artifacts are supplied
// the same way and a contributor who has learned one should not have to learn
// the other.

var (
	loadOnce sync.Once
	loaded   *Model
	loadErr  error
)

// LoadModel reads, verifies, and parses the normative WADL, once per process.
//
// The WADL is supplied by the operator rather than distributed with this
// project; see the NOTICE file and test/conformance/README.md. When no copy is
// available the error wraps ErrNotFound.
//
// Parsing the whole document takes a few tens of milliseconds, so memoising
// keeps the gate cheap enough to apply to every conformance test rather than
// only to a curated few.
func LoadModel() (*Model, error) {
	loadOnce.Do(func() {
		data, _, err := Load()
		if err != nil {
			loadErr = err
			return
		}
		loaded, loadErr = Parse(data)
	})
	return loaded, loadErr
}

// MustLoadModel is LoadModel for tests. It SKIPS the calling test when no WADL
// copy is available, so a contributor without an IEEE copy runs the suite
// green, and it FAILS for every other error, so a wrong, unreadable, or
// unparseable WADL is never mistaken for a pass.
//
// Setting EnvRequired turns the skip into a failure. That is the switch CI
// flips once it supplies a WADL: without it, a rotated secret or a typo in the
// path variable would skip the whole gate and still report green, which is the
// exact failure mode the gate exists to prevent.
func MustLoadModel(t *testing.T) *Model {
	t.Helper()

	// Checked before the load result so a malformed SEP2_WADL_REQUIRED is
	// reported even on a run where a WADL happens to be present.
	required, rerr := Required()
	if rerr != nil {
		t.Fatalf("WADL gate configuration: %v", rerr)
	}

	m, err := LoadModel()
	switch {
	case err == nil:
		return m
	case errors.Is(err, ErrNotFound) && !required:
		t.Skipf("WADL-gated test skipped: %v (set %s=1 to make this a failure)", err, EnvRequired)
	case errors.Is(err, ErrNotFound):
		t.Fatalf("%s is set but no WADL is available: %v", EnvRequired, err)
	default:
		t.Fatalf("load IEEE 2030.5 WADL: %v", err)
	}
	return nil
}
