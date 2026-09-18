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
