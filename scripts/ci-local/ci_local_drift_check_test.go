// #410 -- local gate drift guard tests.
//
// The guard is a shell script (scripts/ci-local-drift-check.sh); tested from
// Go for the same reason as scripts/coverage_gate_test.go: the rest of the
// suite is `go test` and CI already has the toolchain. Each test invokes the
// script against a real or scratch workflow file and checks exit code +
// stdout/stderr against the expected verdict.
package cilocal_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// scriptPath returns an absolute path to scripts/ci-local-drift-check.sh.
func scriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "ci-local-drift-check.sh")
}

// realWorkflowPath returns the repo's actual ci.yml, the one the guard
// checks by default. The happy path exercises the real file rather than a
// fixture copy, so a target added to ci.yml without a matching addition to
// scripts/lib/ci-local-targets.sh fails this test, not just a future CI run.
func realWorkflowPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", ".github", "workflows", "ci.yml")
}

func run(t *testing.T, args ...string) (exit int, out string) {
	t.Helper()
	cmd := exec.Command(scriptPath(t), args...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(b)
		}
		t.Fatalf("exec failed (not ExitError): %v; output=%q", err, string(b))
	}
	return 0, string(b)
}

func TestDriftCheck_RealWorkflowHasNoDrift(t *testing.T) {
	t.Parallel()
	exit, out := run(t, realWorkflowPath(t))
	if exit != 0 {
		t.Fatalf("exit code: got %d, want 0; output=%q", exit, out)
	}
	if !strings.Contains(out, "OK: all 12 make target(s)") {
		t.Fatalf("output missing the expected 12-target OK line; got=%q", out)
	}
}

// TestDriftCheck_CatchesAnInjectedTarget is the "prove the check can fail"
// control for the guard itself: a make target injected into a scratch copy
// of the workflow, absent from CI_LOCAL_MAKE_TARGETS, must be caught and
// named. A guard observed only ever passing is not evidence.
func TestDriftCheck_CatchesAnInjectedTarget(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	original, err := os.ReadFile(realWorkflowPath(t))
	if err != nil {
		t.Fatalf("read real ci.yml: %v", err)
	}
	scratch := filepath.Join(dir, "scratch-ci.yml")
	injected := string(original) + "\n      - name: Scratch drift probe\n        run: make totally-fake-drift-target\n"
	if err := os.WriteFile(scratch, []byte(injected), 0o644); err != nil {
		t.Fatalf("write scratch workflow: %v", err)
	}

	exit, out := run(t, scratch)
	if exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%q", exit, out)
	}
	if !strings.Contains(out, "make totally-fake-drift-target") {
		t.Fatalf("output missing the injected target name; got=%q", out)
	}
	if !strings.Contains(out, "DRIFT: 1 of 13") {
		t.Fatalf("output missing the expected 1-of-13 drift count; got=%q", out)
	}
}

// TestDriftCheck_CatchesAnInjectedTarget_ListItemForm locks in the fix for
// the list-item step form `- run: make X` (no separate `name:` line), which
// the original anchor missed: it accepted an optional `run:` prefix but not
// the leading `- ` a step can carry on the same line. ci.yml does not use
// this form today, but it is idiomatic GitHub Actions and the guard must
// not go blind the day someone adds a step without a name.
func TestDriftCheck_CatchesAnInjectedTarget_ListItemForm(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	original, err := os.ReadFile(realWorkflowPath(t))
	if err != nil {
		t.Fatalf("read real ci.yml: %v", err)
	}
	scratch := filepath.Join(dir, "scratch-ci-listitem.yml")
	injected := string(original) + "\n      - run: make totally-fake-listitem-target\n"
	if err := os.WriteFile(scratch, []byte(injected), 0o644); err != nil {
		t.Fatalf("write scratch workflow: %v", err)
	}

	exit, out := run(t, scratch)
	if exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%q", exit, out)
	}
	if !strings.Contains(out, "make totally-fake-listitem-target") {
		t.Fatalf("output missing the injected target name; got=%q", out)
	}
	if !strings.Contains(out, "DRIFT: 1 of 13") {
		t.Fatalf("output missing the expected 1-of-13 drift count; got=%q", out)
	}
}

// TestDriftCheck_CommentMentionIsNotAnInvocation is the control for the
// exclusion the anchor exists to preserve: ci.yml already comments on a
// make target by name in prose (the CLAUDE.md-referenced remark near its
// test-csip-server step). That comment must never be counted as a second
// invocation; the real ci.yml already reports exactly 12 targets, and this
// test would fail if a future edit to the anchor started counting it.
func TestDriftCheck_CommentMentionIsNotAnInvocation(t *testing.T) {
	t.Parallel()
	original, err := os.ReadFile(realWorkflowPath(t))
	if err != nil {
		t.Fatalf("read real ci.yml: %v", err)
	}
	if !strings.Contains(string(original), "CLAUDE.md-referenced `make test-csip-server`") {
		t.Fatal("ci.yml no longer carries the comment this control relies on; update the control")
	}

	exit, out := run(t, realWorkflowPath(t))
	if exit != 0 {
		t.Fatalf("exit code: got %d, want 0; output=%q", exit, out)
	}
	if !strings.Contains(out, "OK: all 12 make target(s)") {
		t.Fatalf("output missing the expected 12-target OK line (comment mention miscounted?); got=%q", out)
	}
}

func TestDriftCheck_MissingWorkflowFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.yml")
	exit, out := run(t, missing)
	if exit != 2 {
		t.Fatalf("exit code: got %d, want 2; output=%q", exit, out)
	}
	if !strings.Contains(out, "workflow file not found") {
		t.Fatalf("output missing 'workflow file not found'; got=%q", out)
	}
}

// TestDriftCheck_ZeroExtractionRefusesToPass guards the extraction pattern
// itself: a workflow file with no `make` invocations must not report a
// clean pass, since a pattern that stopped matching would look identical to
// a workflow with nothing to check.
func TestDriftCheck_ZeroExtractionRefusesToPass(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	empty := filepath.Join(dir, "no-make-here.yml")
	if err := os.WriteFile(empty, []byte("name: nothing-to-see\non: push\njobs: {}\n"), 0o644); err != nil {
		t.Fatalf("write empty workflow: %v", err)
	}
	exit, out := run(t, empty)
	if exit != 2 {
		t.Fatalf("exit code: got %d, want 2; output=%q", exit, out)
	}
	if !strings.Contains(out, "extracted zero make targets") {
		t.Fatalf("output missing 'extracted zero make targets'; got=%q", out)
	}
}
