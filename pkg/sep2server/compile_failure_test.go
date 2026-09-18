package sep2server_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// wantMutationCall is the exact call testdata/readstorescannotmutate/main.go
// must still attempt. The proof below is worthless once the program stops
// attempting a mutation at all, so that is checked before the build runs.
const wantMutationCall = "reader.MirrorUsagePoints.Create("

// wantRefusalPhrase is the discriminating part of the compiler's message:
// present only when the compiler refuses the call for lacking the method,
// not when a build fails for an unrelated reason (wrong arity, a syntax
// error) that happens to mention "Create" too.
const wantRefusalPhrase = "has no field or method Create"

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

	src, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("read testdata source: %v", err)
	}
	if !strings.Contains(string(src), wantMutationCall) {
		t.Fatalf("testdata/readstorescannotmutate/main.go no longer attempts %q: this test would pass without proving anything", wantMutationCall)
	}

	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "out"), ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("readstorescannotmutate compiled; ReaderStores.MirrorUsagePoints admits Create:\n%s", out)
	}
	// A substring match on "Create" alone is not discriminating: a wrong-arity
	// call to a Create that DOES exist also fails and also names "Create".
	if !strings.Contains(string(out), wantRefusalPhrase) {
		t.Fatalf("build failed for a reason unrelated to the missing write method (want %q): %v\n%s", wantRefusalPhrase, err, out)
	}
}
