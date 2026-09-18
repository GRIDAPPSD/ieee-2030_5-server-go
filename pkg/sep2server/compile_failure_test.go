package sep2server_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadStoresCannotMutate is the compile-failure proof for criterion 2
// of issue #343: ReaderStores' fields are typed as reader interfaces, so a
// caller holding Server.ReadStores() cannot compile a call that mutates the
// store. testdata's readstorescannotmutate program textually attempts a
// Create through the read handle; if ReaderStores.MirrorUsagePoints ever
// widened to a write-capable type, this build would start succeeding and
// this test would catch it turning green. Mirrors the pattern
// pkg/sep2admin/compile_failure_test.go already uses for criterion 3 of
// issue #367.
func TestReadStoresCannotMutate(t *testing.T) {
	dir, err := filepath.Abs(filepath.Join("testdata", "readstorescannotmutate"))
	if err != nil {
		t.Fatalf("resolve testdata dir: %v", err)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "out"), ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("readstorescannotmutate compiled; ReaderStores.MirrorUsagePoints admits Create:\n%s", out)
	}
	if !strings.Contains(string(out), "Create") {
		t.Fatalf("build failed for a reason unrelated to the missing write method (want a message naming Create): %v\n%s", err, out)
	}
}
