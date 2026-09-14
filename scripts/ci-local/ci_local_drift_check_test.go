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
// TestDriftCheck_DefaultArgumentScansTheRealWorkflowDirectory calls the
// script with no argument at all, exercising its own default (the real
// repo's .github/workflows/ directory, all 3 files), not a test-supplied
// single file. This is the exact invocation ci-local.sh itself makes.
func TestDriftCheck_DefaultArgumentScansTheRealWorkflowDirectory(t *testing.T) {
	t.Parallel()
	exit, out := run(t)
	if exit != 0 {
		t.Fatalf("exit code: got %d, want 0; output=%q", exit, out)
	}
	if !strings.Contains(out, "across 2 workflow file(s)") {
		t.Fatalf("output missing 'across 2 workflow file(s)' (expected ci.yml, core-freshness.yml); got=%q", out)
	}
}

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
// TestDriftCheck_CommentMentionIsNotAnInvocation uses a dedicated fixture
// rather than the real ci.yml. An earlier version of this test read
// ci.yml, whose real comment happens to name "make test-csip-server", a
// target already invoked for real elsewhere in that file; sort -u
// collapsed the duplicate, so the assertion never actually depended on
// the comment being excluded and stayed green when the anchor was
// mutated to no longer exclude comments at all. This fixture's comment
// names a target invoked NOWHERE else in it, so a miscount cannot hide
// behind deduplication: the extracted, reported target count is exactly
// 1 (the real invocation) only if the comment was correctly excluded.
func TestDriftCheck_CommentMentionIsNotAnInvocation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fixture := filepath.Join(dir, "comment-fixture.yml")
	content := "name: fixture\non: push\njobs:\n  x:\n    steps:\n      - name: probe\n        run: make h4-real-invocation\n      # a prose mention of make h4-comment-only-mention, never invoked\n"
	if err := os.WriteFile(fixture, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	exit, out := run(t, fixture)
	if exit != 1 {
		t.Fatalf("exit code: got %d, want 1 (h4-real-invocation is real drift, not in CI_LOCAL_MAKE_TARGETS); output=%q", exit, out)
	}
	if !strings.Contains(out, "DRIFT: 1 of 1") {
		t.Fatalf("output missing 'DRIFT: 1 of 1' (comment-only mention miscounted into the denominator?); got=%q", out)
	}
	if !strings.Contains(out, "h4-real-invocation") {
		t.Fatalf("output missing the real invocation's name; got=%q", out)
	}
	if strings.Contains(out, "h4-comment-only-mention") {
		t.Fatalf("output names the comment-only target: the comment was counted as an invocation; got=%q", out)
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
	if !strings.Contains(out, "workflow path not found") {
		t.Fatalf("output missing 'workflow path not found'; got=%q", out)
	}
}

// TestDriftCheck_ZeroExtractionRefusesToPass guards the extraction pattern
// itself: a workflow file with no `make` invocations must not report a
// clean pass, since a pattern that stopped matching would look identical to
// a workflow with nothing to check.
// TestDriftCheck_DefaultScansTheWholeWorkflowDirectory locks in MEDIUM-12:
// called with no argument, the guard must scan every workflow file under
// .github/workflows/, not just ci.yml, so a NEW workflow that invokes an
// uncovered make target raises an alarm. core-freshness.yml already
// invokes 5 targets (all already covered), so the real default-scan
// result should still be a clean OK; a dedicated new-file scenario
// proves the alarm path.
func TestDriftCheck_DefaultScansTheWholeWorkflowDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	workflowsDir := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(workflowsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	covered := "name: covered\non: push\njobs:\n  x:\n    steps:\n      - run: make vet\n"
	uncovered := "name: uncovered\non: push\njobs:\n  y:\n    steps:\n      - run: make m12-new-workflow-target\n"
	if err := os.WriteFile(filepath.Join(workflowsDir, "a.yml"), []byte(covered), 0o644); err != nil {
		t.Fatalf("write a.yml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowsDir, "b.yml"), []byte(uncovered), 0o644); err != nil {
		t.Fatalf("write b.yml: %v", err)
	}

	exit, out := run(t, workflowsDir)
	if exit != 1 {
		t.Fatalf("exit code: got %d, want 1 (b.yml's target is uncovered); output=%q", exit, out)
	}
	if !strings.Contains(out, "m12-new-workflow-target") {
		t.Fatalf("output missing the uncovered target from the second file; got=%q", out)
	}
}

// TestDriftCheck_CrossCheckCatchesBlindSpots locks in MEDIUM-12's second
// half: three forms the anchor cannot structurally parse (make chained
// after &&, a quoted run: scalar, a flow-mapping one-liner) must refuse
// with exit 3 rather than silently reporting OK, since none of them
// leaves the anchored and unanchored counts agreeing.
func TestDriftCheck_CrossCheckCatchesBlindSpots(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
	}{
		{"chained_after_and_and", "        run: make build && make m12-chained-gate"},
		{"quoted_scalar", `        run: "make m12-quoted-gate"`},
		{"flow_mapping", "      - { run: make m12-flow-gate }"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			fixture := filepath.Join(dir, "blindspot.yml")
			content := "name: x\non: push\njobs:\n  x:\n    steps:\n      - name: probe\n" + tc.line + "\n"
			if err := os.WriteFile(fixture, []byte(content), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
			exit, out := run(t, fixture)
			if exit != 3 {
				t.Fatalf("exit code: got %d, want 3 (blind-spot refusal); output=%q", exit, out)
			}
			if !strings.Contains(out, "BLIND SPOT") {
				t.Fatalf("output missing 'BLIND SPOT'; got=%q", out)
			}
		})
	}
}

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
