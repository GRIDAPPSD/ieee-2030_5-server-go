package wadl_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/conformance/wadl"
)

// TestWADLGateArmed is the canary that tells "the gate ran" apart from "the
// gate was skipped", without parsing prose out of go test output. It is the
// single test name to look for: PASS means a WADL was located, verified
// against its digest, and parsed, so the conformance sweep really drove from
// the standard; SKIP means no WADL was available and NOTHING in this module
// walked the server against it.
//
// Under EnvRequired an absent WADL fails here instead of skipping, so a run
// that is supposed to supply a WADL cannot report green without one.
//
// This mirrors TestSchemaGateArmed in the core module. Same purpose, same
// name shape, so one habit covers both artifacts.
func TestWADLGateArmed(t *testing.T) {
	m := wadl.MustLoadModel(t)

	path, err := wadl.Resolve()
	if err != nil {
		t.Fatalf("resolve WADL path after a successful load: %v", err)
	}
	t.Logf("WADL gate armed: %s, %d resources, %d declared methods",
		path, len(m.Resources), len(m.Methods))
}

// TestModelShape is the load-bearing guard on the parser. If the parser ever
// stops understanding a construct the WADL uses, these counts move and the
// failure names the parser rather than showing up later as a mysteriously
// clean conformance sweep.
//
// A sweep that silently parsed half the document would report half the
// defects and look like an improvement.
//
// These are aggregate counts over the document, deliberately NOT a
// transcription of its rows: the WADL is not distributed with this project
// and must not be reconstructable from anything committed here.
func TestModelShape(t *testing.T) {
	m := wadl.MustLoadModel(t)

	if got, want := len(m.Resources), 126; got != want {
		t.Errorf("resource count = %d, want %d; this is not the expected WADL", got, want)
	}
	if got, want := len(m.Methods), 630; got != want {
		t.Errorf("declared method count = %d, want %d; this is not the expected WADL", got, want)
	}

	// The mode split decides whether a live observation is a defect or a
	// design choice, so a drift here silently reclassifies findings.
	counts := m.ModeCounts()
	for _, tc := range []struct {
		mode wadl.Mode
		want int
	}{
		{wadl.ModeMandatory, 258},
		{wadl.ModeOptional, 79},
		{wadl.ModeDeprecated, 58},
		{wadl.ModeError, 235},
	} {
		if got := counts[tc.mode]; got != tc.want {
			t.Errorf("mode %s (%s) count = %d, want %d", tc.mode, tc.mode.Meaning(), got, tc.want)
		}
	}

	total := 0
	for _, n := range counts {
		total += n
	}
	if total != len(m.Methods) {
		t.Errorf("mode counts sum to %d but there are %d methods, so some method carries no wx:mode", total, len(m.Methods))
	}
}

// TestKnownDeclarations spot-checks a handful of declarations by hand.
//
// These are hand-authored assertions about specific, named routes, which is
// what keeps the parser honest without committing a mirror of the document.
// Each one is a fact a reader can look up in the standard and check.
func TestKnownDeclarations(t *testing.T) {
	m := wadl.MustLoadModel(t)

	index := map[string]wadl.Method{}
	for _, meth := range m.Methods {
		index[meth.Verb+" "+meth.Path] = meth
	}

	tests := []struct {
		key          string
		wantMode     wadl.Mode
		wantElements []string
		wantStatuses []int
		wantLocation bool
	}{
		// The three resources every conforming server must serve.
		{key: "GET /dcap", wantMode: wadl.ModeMandatory, wantElements: []string{"DeviceCapability"}},
		{key: "GET /tm", wantMode: wadl.ModeMandatory, wantElements: []string{"Time"}},
		{key: "GET /sdev", wantMode: wadl.ModeMandatory, wantElements: []string{"SelfDevice"}},
		{key: "GET /edev", wantMode: wadl.ModeMandatory, wantElements: []string{"EndDeviceList"}},
		{key: "GET /edev/{id1}", wantMode: wadl.ModeMandatory, wantElements: []string{"EndDevice"}},

		// Client registration: POST /edev is Optional, and when it succeeds
		// the WADL requires a Location header pointing at the new resource.
		{key: "POST /edev", wantMode: wadl.ModeOptional, wantStatuses: []int{200, 201}, wantLocation: true},

		// E-mode: the method must be refused. A server that accepts one of
		// these is not merely unhelpful, it contradicts the standard.
		{key: "DELETE /dcap", wantMode: wadl.ModeError, wantStatuses: []int{400, 405}},
		{key: "PUT /dcap", wantMode: wadl.ModeError, wantStatuses: []int{400, 405}},
		{key: "POST /tm", wantMode: wadl.ModeError, wantStatuses: []int{400, 405}},
	}

	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			got, ok := index[tc.key]
			if !ok {
				t.Fatalf("the WADL declares no %q", tc.key)
			}
			if got.Mode != tc.wantMode {
				t.Errorf("mode = %s (%s), want %s (%s)",
					got.Mode, got.Mode.Meaning(), tc.wantMode, tc.wantMode.Meaning())
			}
			if tc.wantElements != nil && !equalStrings(got.DeclaredElements, tc.wantElements) {
				t.Errorf("declared elements = %v, want %v", got.DeclaredElements, tc.wantElements)
			}
			if tc.wantStatuses != nil && !equalInts(got.DeclaredStatuses, tc.wantStatuses) {
				t.Errorf("declared statuses = %v, want %v", got.DeclaredStatuses, tc.wantStatuses)
			}
			if got.RequiresLocationHeader != tc.wantLocation {
				t.Errorf("RequiresLocationHeader = %v, want %v", got.RequiresLocationHeader, tc.wantLocation)
			}
		})
	}
}

// TestTemplatedPaths asserts the parser preserves the {idN} slots the sweep
// concretizes. A parser that dropped or renamed them would make every
// instance path unreachable and report the whole instance surface as
// unrouted.
func TestTemplatedPaths(t *testing.T) {
	m := wadl.MustLoadModel(t)

	templated := 0
	for _, meth := range m.Methods {
		if meth.Templated() {
			templated++
		}
	}
	if templated == 0 {
		t.Fatal("no templated paths found; the sweep would never exercise an instance resource")
	}
	t.Logf("%d of %d declared methods carry an {idN} slot", templated, len(m.Methods))
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
