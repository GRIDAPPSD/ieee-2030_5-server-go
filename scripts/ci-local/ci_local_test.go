// #410 -- local gate entry point: lightweight wiring tests.
//
// The tri-state exit contract and fail-fast/skip behaviour are exercised
// end to end against a disposable scratch repo in ci_local_e2e_test.go
// (CRITICAL-1: an earlier version of this file ran only against the
// real Makefile, which nothing but a manual smoke test ever executed).
// This file stays scoped to the cheap checks: the scripts parse as valid
// bash, both entry points are executable, and the shared target list
// both scripts depend on has not silently changed shape.
package cilocal_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func TestScripts_ParseAsBash(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{
		"scripts/ci-local/ci-local.sh",
		"scripts/ci-local/ci-local-drift-check.sh",
		"scripts/ci-local/lib/ci-local-targets.sh",
	} {
		path := filepath.Join(root, rel)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		cmd := exec.Command("bash", "-n", path)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("bash -n %s: %v; output=%q", rel, err, string(out))
		}
	}
}

func TestScripts_EntryPointsAreExecutable(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	for _, rel := range []string{"scripts/ci-local/ci-local.sh", "scripts/ci-local/ci-local-drift-check.sh"} {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if info.Mode()&0o111 == 0 {
			t.Fatalf("%s is not executable (mode %s)", rel, info.Mode())
		}
	}
}

// TestSharedTargetList_MatchesWhatCILocalRuns sources the shared lib in a
// throwaway bash process and prints CI_LOCAL_MAKE_TARGETS, so a future edit
// that changes the run order or drops a target is caught here rather than
// only showing up as a drift-check surprise or a silently-shortened run.
func TestSharedTargetList_MatchesWhatCILocalRuns(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	lib := filepath.Join(root, "scripts", "ci-local", "lib", "ci-local-targets.sh")
	cmd := exec.Command("bash", "-c", "source \"$1\" && printf '%s\\n' \"${CI_LOCAL_MAKE_TARGETS[@]}\"", "--", lib)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("source %s: %v; output=%q", lib, err, string(out))
	}
	got := strings.Fields(strings.TrimSpace(string(out)))
	want := []string{
		"vet", "build", "lint", "test", "test-race", "test-cover",
		"test-csip", "test-csip-server", "test-csip-hooks", "test-csip-race",
		"test-csip-cover", "coverage-gate", "ui-check",
	}
	if len(got) != len(want) {
		t.Fatalf("target count: got %d %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("target[%d]: got %q, want %q (full list got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
}
