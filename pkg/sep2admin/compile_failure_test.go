package sep2admin_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestGraftCannotNameTheCoreBand is the compile-failure proof for
// criterion 3: Placement.group is unexported, so ExtensionSlot is the only
// way to obtain a Placement from outside this package. testdata's
// graftcannotsetgroup program textually attempts to set group directly;
// if group were ever exported, or gained an exported alias, this build
// would start succeeding and this test would catch it turning green.
func TestGraftCannotNameTheCoreBand(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "graftcannotsetgroup"))
	if err != nil {
		t.Fatalf("resolve testdata dir: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "out"), ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("graftcannotsetgroup compiled; Placement.group is reachable from outside package sep2admin:\n%s", out)
	}
	if !strings.Contains(string(out), "group") {
		t.Fatalf("build failed for a reason unrelated to the unexported field (want a message naming group): %v\n%s", err, out)
	}
}

// TestGraftCannotImplementBody is the compile-failure proof for #368 item
// 2: Body is sealed to this package by its unexported bodyKind method, so
// a Descriptor can only ever carry a TableBody, a DefinitionListBody, or
// nil, never a third shape a caller invented. testdata's
// graftcannotimplementbody program declares a same-named bodyKind method
// on a type in another package; if that ever started compiling, the seal
// would be broken and a caller could smuggle an arbitrary shape into
// Descriptor.Body.
func TestGraftCannotImplementBody(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "graftcannotimplementbody"))
	if err != nil {
		t.Fatalf("resolve testdata dir: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "out"), ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("graftcannotimplementbody compiled; Body is reachable from outside package sep2admin:\n%s", out)
	}
	if !strings.Contains(string(out), "bodyKind") {
		t.Fatalf("build failed for a reason unrelated to the unexported method (want a message naming bodyKind): %v\n%s", err, out)
	}
}
