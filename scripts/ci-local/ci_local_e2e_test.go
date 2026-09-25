// #410 -- end-to-end harness for scripts/ci-local/ci-local.sh.
//
// CRITICAL-1 review finding: the tri-state exit contract (0 clean / 1
// failure / 2 partial), the fail-fast stop-at-first-failure behaviour, and
// the SKIPPED-vs-PASSED distinction are the script's central design
// claims, and until this file existed nothing but a manual smoke test
// (see the card/PR record) exercised them. This harness runs the real
// script against a disposable scratch repo with a stub Makefile, so the
// contract is asserted by `go test`, not by a human rereading terminal
// output.
//
// The scratch repo is real (git init'd, one commit) rather than faked,
// because run_ui_check_gate's restore logic is real git plumbing
// (checkout, clean, status --porcelain) and there is no faithful way to
// stub that without just running it against a real repository.
package cilocal_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scriptPath returns the absolute path to scripts/ci-local/ci-local.sh.
func ciLocalScriptPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot(t), "scripts", "ci-local", "ci-local.sh")
}

// writeScratchLib writes a CI_LOCAL_MAKE_TARGETS/CI_LOCAL_TARGET_PREREQ/
// CI_LOCAL_ONLY_TARGETS lib to dir/lib.sh and returns its path. prereqs
// maps each target to its prerequisite class; a target present in
// targets but absent from prereqs is deliberately omittable, to exercise
// MEDIUM-9's refusal.
func writeScratchLib(t *testing.T, dir string, targets []string, prereqs map[string]string) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("CI_LOCAL_MAKE_TARGETS=(\n")
	for _, tgt := range targets {
		fmt.Fprintf(&sb, "  %s\n", tgt)
	}
	sb.WriteString(")\n\ndeclare -A CI_LOCAL_TARGET_PREREQ=(\n")
	for tgt, class := range prereqs {
		fmt.Fprintf(&sb, "  [%s]=%s\n", tgt, class)
	}
	sb.WriteString(")\n\ndeclare -A CI_LOCAL_ONLY_TARGETS=()\n")

	path := filepath.Join(dir, "lib.sh")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write scratch lib: %v", err)
	}
	return path
}

// writeScratchWorkflow writes a workflow file whose steps invoke exactly
// the given make targets, one `run: make X` per target, so the drift
// guard passes cleanly against writeScratchLib's matching target list.
func writeScratchWorkflow(t *testing.T, dir string, targets []string) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("name: scratch\non: push\njobs:\n  x:\n    steps:\n")
	for _, tgt := range targets {
		fmt.Fprintf(&sb, "      - run: make %s\n", tgt)
	}
	path := filepath.Join(dir, "workflow.yml")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write scratch workflow: %v", err)
	}
	return path
}

// scratchMakefile is shared by every scenario below: always-pass exits 0,
// always-fail exits 1, maybe-skip just needs to run (its prerequisite
// class controls whether it is reached at all), and ui-check simulates a
// frontend rebuild by touching a file under dist/, then exits 0 unless
// UI_CHECK_SHOULD_FAIL is set.
const scratchMakefile = `
always-pass:
	@true

always-fail:
	@false

maybe-skip:
	@echo maybe-skip ran

csip-like-target:
	@if [ "$$CSIP_SUNSPEC_REQUIRED" != "1" ]; then echo "CSIP_SUNSPEC_REQUIRED not propagated: got '$$CSIP_SUNSPEC_REQUIRED'"; exit 1; fi
	@echo csip-like-target ran with CSIP_SUNSPEC_REQUIRED=1

ui-check:
	@mkdir -p pkg/adminui/web/dist
	@echo rebuilt-$$(date +%s%N) > pkg/adminui/web/dist/bundle.txt
	@if [ -n "$$UI_CHECK_SHOULD_FAIL" ]; then exit 1; fi
`

// newScratchRepo creates a disposable git repo with the shared Makefile
// and a committed pkg/adminui/web/dist/bundle.txt, so run_ui_check_gate's
// git status/checkout/clean plumbing has a real tracked path to operate
// on. A minimal go.mod pins goVersion (check_toolchain reads it from
// REPO_ROOT/go.mod); pass "" to omit it (indeterminate-toolchain path).
func newScratchRepo(t *testing.T, goVersion string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(name string, args ...string) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v; output=%s", name, args, err, out)
		}
	}
	run("git", "init", "--quiet")
	run("git", "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--allow-empty", "--quiet", "-m", "init")

	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(scratchMakefile), 0o644); err != nil {
		t.Fatalf("write Makefile: %v", err)
	}
	if goVersion != "" {
		mod := fmt.Sprintf("module scratch\n\ngo %s\n", goVersion)
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0o644); err != nil {
			t.Fatalf("write go.mod: %v", err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "internal", "server", "web", "dist"), 0o755); err != nil {
		t.Fatalf("mkdir dist: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "server", "web", "dist", "bundle.txt"), []byte("committed\n"), 0o644); err != nil {
		t.Fatalf("write bundle.txt: %v", err)
	}
	run("git", "add", "-A")
	run("git", "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "--quiet", "-m", "seed")
	return dir
}

// runCILocalResult is a black-box invocation: exit code, full combined
// output, and a convenience split into per-line records for callers that
// need to check attempt order (fail-fast).
type runCILocalResult struct {
	exit   int
	output string
}

// runCILocal invokes ci-local.sh with the three test-only overrides
// pointed at a scratch environment, plus whatever extra env the caller
// needs (e.g. a stubbed PATH for HIGH-5's prereq flip).
func runCILocal(t *testing.T, repo, lib, workflow string, extraEnv ...string) runCILocalResult {
	t.Helper()
	cmd := exec.Command(ciLocalScriptPath(t))
	cmd.Env = append(os.Environ(),
		"CI_LOCAL_REPO_ROOT="+repo,
		"CI_LOCAL_TARGETS_LIB="+lib,
		"CI_LOCAL_WORKFLOW_FILE="+workflow,
	)
	cmd.Env = append(cmd.Env, extraEnv...)
	out, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			t.Fatalf("exec ci-local.sh: %v; output=%s", err, out)
		}
	}
	return runCILocalResult{exit: exit, output: string(out)}
}

func TestCILocalEndToEnd_AllPass(t *testing.T) {
	t.Parallel()
	targets := []string{"always-pass", "maybe-skip"}
	prereqs := map[string]string{"always-pass": "none", "maybe-skip": "none"}
	dir := t.TempDir()
	repo := newScratchRepo(t, "")
	lib := writeScratchLib(t, dir, targets, prereqs)
	workflow := writeScratchWorkflow(t, dir, targets)

	res := runCILocal(t, repo, lib, workflow)

	if res.exit != 0 {
		t.Fatalf("exit code: got %d, want 0; output=%s", res.exit, res.output)
	}
	if !strings.Contains(res.output, "CI_LOCAL_RESULT=PASS") {
		t.Fatalf("output missing CI_LOCAL_RESULT=PASS; output=%s", res.output)
	}
	if !strings.Contains(res.output, "2 PASSED, 0 SKIPPED, 0 FAILED (2 of 2 gates attempted)") {
		t.Fatalf("output missing the expected gate count line; output=%s", res.output)
	}
}

func TestCILocalEndToEnd_OneFail(t *testing.T) {
	t.Parallel()
	// always-fail is ordered before a target that must never be reached:
	// fail-fast means "unreached-target" is neither PASSED nor SKIPPED
	// nor FAILED anywhere in the output.
	targets := []string{"always-pass", "always-fail", "unreached-target"}
	prereqs := map[string]string{"always-pass": "none", "always-fail": "none", "unreached-target": "none"}
	dir := t.TempDir()
	repo := newScratchRepo(t, "")
	lib := writeScratchLib(t, dir, targets, prereqs)
	workflow := writeScratchWorkflow(t, dir, targets)

	res := runCILocal(t, repo, lib, workflow)

	if res.exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%s", res.exit, res.output)
	}
	if !strings.Contains(res.output, "CI_LOCAL_RESULT=FAIL") {
		t.Fatalf("output missing CI_LOCAL_RESULT=FAIL; output=%s", res.output)
	}
	if !strings.Contains(res.output, "FAILED  always-fail") {
		t.Fatalf("output missing the named failing gate; output=%s", res.output)
	}
	// unreached-target legitimately appears once, in the up-front plan
	// listing ("Gates to run (3)..."); fail-fast means it never gets a
	// per-gate status line after that.
	for _, status := range []string{"PASSED  unreached-target", "SKIPPED unreached-target", "FAILED  unreached-target"} {
		if strings.Contains(res.output, status) {
			t.Fatalf("fail-fast violated: %q appears in output; output=%s", status, res.output)
		}
	}
	if !strings.Contains(res.output, "1 PASSED, 0 SKIPPED, 1 FAILED (2 of 3 gates attempted)") {
		t.Fatalf("output missing the expected attempted-vs-total line; output=%s", res.output)
	}
}

func TestCILocalEndToEnd_OneSkip(t *testing.T) {
	t.Parallel()
	targets := []string{"always-pass", "maybe-skip"}
	// golangci-lint reuses the script's real detection (command -v
	// golangci-lint), forced absent by a minimal stub PATH below.
	prereqs := map[string]string{"always-pass": "none", "maybe-skip": "golangci-lint"}
	dir := t.TempDir()
	repo := newScratchRepo(t, "")
	lib := writeScratchLib(t, dir, targets, prereqs)
	workflow := writeScratchWorkflow(t, dir, targets)
	stubPath := writeStubPathWithout(t, "golangci-lint")

	res := runCILocal(t, repo, lib, workflow, "PATH="+stubPath)

	if res.exit != 2 {
		t.Fatalf("exit code: got %d, want 2; output=%s", res.exit, res.output)
	}
	if !strings.Contains(res.output, "CI_LOCAL_RESULT=PARTIAL") {
		t.Fatalf("output missing CI_LOCAL_RESULT=PARTIAL; output=%s", res.output)
	}
	if !strings.Contains(res.output, "SKIPPED maybe-skip") {
		t.Fatalf("output missing the named skipped gate; output=%s", res.output)
	}
	if !strings.Contains(res.output, "1 PASSED, 1 SKIPPED, 0 FAILED (2 of 2 gates attempted)") {
		t.Fatalf("output missing the expected gate count line; output=%s", res.output)
	}
}

// writeStubPathWithout builds a minimal PATH: symlinks to the real
// bash/make/git/go/coreutils the script needs, deliberately excluding
// the named command, so absence is deterministic regardless of what is
// actually installed on the host running this test.
func writeStubPathWithout(t *testing.T, exclude string) string {
	t.Helper()
	bin := t.TempDir()
	for _, tool := range []string{"bash", "make", "git", "go", "sh", "cat", "sed", "grep", "sort", "tail", "awk", "mkdir", "rm", "printf", "true", "false", "echo", "env", "date", "tr", "find", "mktemp", "wc"} {
		if tool == exclude {
			continue
		}
		real, err := exec.LookPath(tool)
		if err != nil {
			continue
		}
		if err := os.Symlink(real, filepath.Join(bin, tool)); err != nil {
			t.Fatalf("symlink %s: %v", tool, err)
		}
	}
	return bin
}

// writeStubPathWith is writeStubPathWithout plus a fake `name` shim that
// exits 0, so a prerequisite-class detector reports present. Used by
// TestCILocalEndToEnd_PrereqFlipsFromAbsentToPresent (HIGH-5).
func writeStubPathWith(t *testing.T, name string) string {
	t.Helper()
	bin := writeStubPathWithout(t, "")
	shim := filepath.Join(bin, name)
	if err := os.WriteFile(shim, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write shim %s: %v", name, err)
	}
	return bin
}

// TestCILocalEndToEnd_PrereqFlipsFromAbsentToPresent is HIGH-5: nothing
// previously flipped a prerequisite from absent to present, so a
// detector hardcoded to always report absent (SKIPPED unconditionally)
// would have stayed green. Same scratch env, two runs, two different
// PATHs, two different outcomes: the second run must actually attempt
// the gate rather than skip it.
func TestCILocalEndToEnd_PrereqFlipsFromAbsentToPresent(t *testing.T) {
	t.Parallel()
	targets := []string{"maybe-skip"}
	prereqs := map[string]string{"maybe-skip": "golangci-lint"}
	dir := t.TempDir()
	repo := newScratchRepo(t, "")
	lib := writeScratchLib(t, dir, targets, prereqs)
	workflow := writeScratchWorkflow(t, dir, targets)

	absentPath := writeStubPathWithout(t, "golangci-lint")
	absent := runCILocal(t, repo, lib, workflow, "PATH="+absentPath)
	if !strings.Contains(absent.output, "SKIPPED maybe-skip") {
		t.Fatalf("absent run: expected SKIPPED maybe-skip; output=%s", absent.output)
	}

	presentPath := writeStubPathWith(t, "golangci-lint")
	present := runCILocal(t, repo, lib, workflow, "PATH="+presentPath)
	if strings.Contains(present.output, "SKIPPED maybe-skip") {
		t.Fatalf("present run: still reports SKIPPED maybe-skip with golangci-lint on PATH; output=%s", present.output)
	}
	if !strings.Contains(present.output, "PASSED  maybe-skip") {
		t.Fatalf("present run: expected the gate to actually run and pass; output=%s", present.output)
	}
}

// TestCILocalEndToEnd_UiCheckPassRestoresCleanly is HIGH-6 (pass path):
// run_ui_check_gate is exercised through the real script, not left
// uninvoked by every test. dist/ must be clean again afterward.
// TestCILocalEndToEnd_CSIPFixturesDetectedViaEnvVars is HIGH-2: the
// harness resolves the SunSpec PKI env-var-first (test/csip/fixture_gate_test.go),
// so detection here must honour CSIP_SUNSPEC_CERT/KEY/ROOTS or it can
// decide "absent" (SKIPPED) on a host that supplies the material purely
// by environment, silently skipping coverage-gate's 80% floor along with
// the rest. Two runs: no env and no fixture files (SKIPPED), then the
// three env vars pointing at real files elsewhere (must run, not skip),
// with the scratch recipe itself asserting CSIP_SUNSPEC_REQUIRED=1 was
// exported into its environment.
func TestCILocalEndToEnd_CSIPFixturesDetectedViaEnvVars(t *testing.T) {
	t.Parallel()
	targets := []string{"csip-like-target"}
	prereqs := map[string]string{"csip-like-target": "csip-fixtures"}
	dir := t.TempDir()
	repo := newScratchRepo(t, "")
	lib := writeScratchLib(t, dir, targets, prereqs)
	workflow := writeScratchWorkflow(t, dir, targets)

	absent := runCILocal(t, repo, lib, workflow)
	if !strings.Contains(absent.output, "SKIPPED csip-like-target") {
		t.Fatalf("absent run: expected SKIPPED csip-like-target (no env, no fixture files); output=%s", absent.output)
	}

	pemDir := t.TempDir()
	for _, leaf := range []string{"cert.pem", "key.pem", "roots.pem"} {
		if err := os.WriteFile(filepath.Join(pemDir, leaf), []byte("dummy\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", leaf, err)
		}
	}
	present := runCILocal(t, repo, lib, workflow,
		"CSIP_SUNSPEC_CERT="+filepath.Join(pemDir, "cert.pem"),
		"CSIP_SUNSPEC_KEY="+filepath.Join(pemDir, "key.pem"),
		"CSIP_SUNSPEC_ROOTS="+filepath.Join(pemDir, "roots.pem"),
	)
	if strings.Contains(present.output, "SKIPPED csip-like-target") {
		t.Fatalf("env-provisioned run: still reports SKIPPED with all three env vars set; output=%s", present.output)
	}
	if !strings.Contains(present.output, "PASSED  csip-like-target") {
		t.Fatalf("env-provisioned run: expected the gate to run and pass (recipe asserts CSIP_SUNSPEC_REQUIRED=1 itself); output=%s", present.output)
	}
}

func TestCILocalEndToEnd_UiCheckPassRestoresCleanly(t *testing.T) {
	t.Parallel()
	targets := []string{"ui-check"}
	prereqs := map[string]string{"ui-check": "none"}
	dir := t.TempDir()
	repo := newScratchRepo(t, "")
	lib := writeScratchLib(t, dir, targets, prereqs)
	workflow := writeScratchWorkflow(t, dir, targets)

	res := runCILocal(t, repo, lib, workflow)

	if res.exit != 0 {
		t.Fatalf("exit code: got %d, want 0; output=%s", res.exit, res.output)
	}
	if !strings.Contains(res.output, "PASSED  ui-check") {
		t.Fatalf("output missing PASSED ui-check; output=%s", res.output)
	}
	status := gitStatusPorcelain(t, repo, "pkg/adminui/web/dist")
	if status != "" {
		t.Fatalf("dist/ left dirty after a passing ui-check run: %q", status)
	}
}

// TestCILocalEndToEnd_UiCheckFailReportsAndDoesNotHeal is HIGH-6 (fail
// path) plus MEDIUM-7's regression guard: a failing recipe must report
// FAILED, and running the gate again with zero developer action must
// NOT silently turn green (the original bug: run 1 failed and cleaned
// dist/, run 2 reported PASSED with nothing changed).
func TestCILocalEndToEnd_UiCheckFailReportsAndDoesNotHeal(t *testing.T) {
	t.Parallel()
	targets := []string{"ui-check"}
	prereqs := map[string]string{"ui-check": "none"}
	dir := t.TempDir()
	repo := newScratchRepo(t, "")
	lib := writeScratchLib(t, dir, targets, prereqs)
	workflow := writeScratchWorkflow(t, dir, targets)

	first := runCILocal(t, repo, lib, workflow, "UI_CHECK_SHOULD_FAIL=1")
	if first.exit != 1 {
		t.Fatalf("run 1: exit code: got %d, want 1; output=%s", first.exit, first.output)
	}
	if !strings.Contains(first.output, "FAILED  ui-check") {
		t.Fatalf("run 1: output missing FAILED ui-check; output=%s", first.output)
	}

	// Same env, zero developer action, UI_CHECK_SHOULD_FAIL still set:
	// must fail again, not heal.
	second := runCILocal(t, repo, lib, workflow, "UI_CHECK_SHOULD_FAIL=1")
	if second.exit != 1 {
		t.Fatalf("run 2 healed: exit code: got %d, want 1 (regression: MEDIUM-7 self-healing bug); output=%s", second.exit, second.output)
	}
	if !strings.Contains(second.output, "FAILED  ui-check") {
		t.Fatalf("run 2 healed: output missing FAILED ui-check; output=%s", second.output)
	}
}

// TestCILocalEndToEnd_UiCheckRefusesOverPreExistingDirt is MEDIUM-7's
// destructive-revert guard: a real uncommitted change under dist/ that
// predates this run must never be silently reverted.
func TestCILocalEndToEnd_UiCheckRefusesOverPreExistingDirt(t *testing.T) {
	t.Parallel()
	targets := []string{"ui-check"}
	prereqs := map[string]string{"ui-check": "none"}
	dir := t.TempDir()
	repo := newScratchRepo(t, "")
	lib := writeScratchLib(t, dir, targets, prereqs)
	workflow := writeScratchWorkflow(t, dir, targets)

	precious := filepath.Join(repo, "internal", "server", "web", "dist", "bundle.txt")
	if err := os.WriteFile(precious, []byte("uncommitted developer work\n"), 0o644); err != nil {
		t.Fatalf("dirty the tree: %v", err)
	}

	res := runCILocal(t, repo, lib, workflow)

	if res.exit != 1 {
		t.Fatalf("exit code: got %d, want 1 (refusal); output=%s", res.exit, res.output)
	}
	if !strings.Contains(res.output, "pre-existing uncommitted changes") {
		t.Fatalf("output missing the refusal reason; output=%s", res.output)
	}
	got, err := os.ReadFile(precious)
	if err != nil {
		t.Fatalf("read bundle.txt after refusal: %v", err)
	}
	if string(got) != "uncommitted developer work\n" {
		t.Fatalf("pre-existing dirt was altered despite the refusal: got %q", string(got))
	}
}

func gitStatusPorcelain(t *testing.T, repo, path string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "status", "--porcelain", "--", path)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status --porcelain -- %s: %v; output=%s", path, err, out)
	}
	return string(out)
}

// TestCILocalEndToEnd_RefusesWhenPrereqMapIncomplete is MEDIUM-9: a
// target present in CI_LOCAL_MAKE_TARGETS with no matching
// CI_LOCAL_TARGET_PREREQ entry must refuse to start, not silently run
// with the prerequisite degraded to "none".
func TestCILocalEndToEnd_RefusesWhenPrereqMapIncomplete(t *testing.T) {
	t.Parallel()
	targets := []string{"always-pass", "unmapped-target"}
	prereqs := map[string]string{"always-pass": "none"} // unmapped-target deliberately omitted
	dir := t.TempDir()
	repo := newScratchRepo(t, "")
	lib := writeScratchLib(t, dir, targets, prereqs)
	workflow := writeScratchWorkflow(t, dir, targets)

	res := runCILocal(t, repo, lib, workflow)

	if res.exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%s", res.exit, res.output)
	}
	if !strings.Contains(res.output, "unmapped-target") {
		t.Fatalf("output missing the unmapped target's name; output=%s", res.output)
	}
	if strings.Contains(res.output, "PASSED  always-pass") {
		t.Fatalf("gate loop ran despite the incomplete prerequisite map; output=%s", res.output)
	}
}

// TestCILocalEndToEnd_ToolchainMismatchFailsTheRun is MEDIUM-10: a Go
// version pin that differs from the local toolchain must fail the run
// (fold into the exit code), not just print a line nothing acts on.
func TestCILocalEndToEnd_ToolchainMismatchFailsTheRun(t *testing.T) {
	t.Parallel()
	targets := []string{"always-pass"}
	prereqs := map[string]string{"always-pass": "none"}
	dir := t.TempDir()
	// A version no real toolchain will ever report, guaranteeing a
	// mismatch against whatever `go env GOVERSION` says on this host.
	repo := newScratchRepo(t, "1.0.0")
	lib := writeScratchLib(t, dir, targets, prereqs)
	workflow := writeScratchWorkflow(t, dir, targets)

	res := runCILocal(t, repo, lib, workflow)

	if res.exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%s", res.exit, res.output)
	}
	if !strings.Contains(res.output, "CI_LOCAL_RESULT=FAIL") {
		t.Fatalf("output missing CI_LOCAL_RESULT=FAIL; output=%s", res.output)
	}
	if !strings.Contains(res.output, "TOOLCHAIN: MISMATCH") {
		t.Fatalf("output missing TOOLCHAIN: MISMATCH; output=%s", res.output)
	}
	if strings.Contains(res.output, "PASSED  always-pass") {
		t.Fatalf("gate loop ran despite the toolchain mismatch; output=%s", res.output)
	}
}
