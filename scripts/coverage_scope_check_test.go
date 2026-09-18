// #387 - Coverage-scope-check guard unit tests.
//
// The guard is a shell script (scripts/coverage-scope-check.sh); tested
// from Go for the same reason as coverage_gate_test.go: the rest of the
// suite is `go test` and CI already has the toolchain on the runner.
//
// The isolated cases build a small synthetic Go module in a temp
// directory, so they do not depend on this module's own package layout.
// The "real entries" case runs the guard from this module's own root
// against its live CSIP_COVERPKG value, read straight out of the
// Makefile, so it always exercises what `make test-csip-cover` runs.
//
// Package name: this directory holds no non-test Go source, so Go treats
// whichever external test package name is declared here as the package
// for the whole directory. It must match coverage_gate_test.go's
// `coveragegate_test`, not a name of its own, or the two test files
// conflict as two different packages in one directory.
package coveragegate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// scopeCheckScript returns coverage-scope-check.sh's absolute path. Named
// distinctly from coverage_gate_test.go's scriptPath: both files share
// this package (see the note above the package clause).
func scopeCheckScript(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "coverage-scope-check.sh")
}

// moduleRoot is this module's own root: two directories up from this test
// file's scripts/ directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	return filepath.Dir(filepath.Dir(scopeCheckScript(t)))
}

// newSyntheticModule builds a minimal Go module in a temp directory with
// one real package (pkgs/real) and one directory that holds no .go files
// (pkgs/empty), so a test can exercise the guard against controlled
// content instead of this repo's own layout.
func newSyntheticModule(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "go.mod"), "module scopecheck\n\ngo 1.21\n")
	mustMkdir(t, filepath.Join(dir, "pkgs", "real"))
	mustWrite(t, filepath.Join(dir, "pkgs", "real", "real.go"), "package real\n")
	mustMkdir(t, filepath.Join(dir, "pkgs", "empty"))
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// runScopeCheck executes the guard with the given coverpkg pattern from
// dir and returns its exit code and combined output.
func runScopeCheck(t *testing.T, dir, coverpkg string) (int, string) {
	t.Helper()
	cmd := exec.Command(scopeCheckScript(t), coverpkg)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	exit := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("exec failed (not ExitError): %v; output=%q", err, string(out))
		}
		exit = ee.ExitCode()
	}
	return exit, string(out)
}

func TestCoverageScopeCheck_WildcardMatchesNoPackage(t *testing.T) {
	t.Parallel()
	dir := newSyntheticModule(t)

	exit, out := runScopeCheck(t, dir, "./pkgs/empty/...")

	if exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%q", exit, out)
	}
	if !strings.Contains(out, "pattern resolves to no package: ./pkgs/empty/...") {
		t.Fatalf("output missing the no-package message; got=%q", out)
	}
}

func TestCoverageScopeCheck_AbsentDirectory(t *testing.T) {
	t.Parallel()
	dir := newSyntheticModule(t)

	exit, out := runScopeCheck(t, dir, "./pkgs/nope/...")

	if exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%q", exit, out)
	}
	if !strings.Contains(out, "pattern resolves to no package: ./pkgs/nope/...") {
		t.Fatalf("output missing the no-package message; got=%q", out)
	}
}

func TestCoverageScopeCheck_EmptyList(t *testing.T) {
	t.Parallel()
	dir := newSyntheticModule(t)

	exit, out := runScopeCheck(t, dir, "")

	if exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%q", exit, out)
	}
	if !strings.Contains(out, "pattern list is empty") {
		t.Fatalf("output missing the empty-list message; got=%q", out)
	}
}

func TestCoverageScopeCheck_WhitespaceOnlyList(t *testing.T) {
	t.Parallel()
	dir := newSyntheticModule(t)

	exit, out := runScopeCheck(t, dir, "   ")

	if exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%q", exit, out)
	}
	if !strings.Contains(out, "pattern list is empty") {
		t.Fatalf("output missing the empty-list message; got=%q", out)
	}
}

// A trailing comma must not be silently dropped: `read -a` on a
// here-string drops the trailing empty field it produces, and an empty
// pattern resolves to no package.
func TestCoverageScopeCheck_TrailingEmptyField(t *testing.T) {
	t.Parallel()
	dir := newSyntheticModule(t)

	exit, out := runScopeCheck(t, dir, "./pkgs/real,")

	if exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%q", exit, out)
	}
	if !strings.Contains(out, "pattern resolves to no package: ") {
		t.Fatalf("output missing the no-package message for the trailing field; got=%q", out)
	}
}

// A leading or interior empty field is already surfaced without any fix in
// this round: `read -a` keeps it in the parsed array, and `go list ""`
// itself fails. This pins that down so a future change cannot regress it.
func TestCoverageScopeCheck_LeadingEmptyField(t *testing.T) {
	t.Parallel()
	dir := newSyntheticModule(t)

	exit, out := runScopeCheck(t, dir, ",./pkgs/real")

	if exit != 1 {
		t.Fatalf("exit code: got %d, want 1; output=%q", exit, out)
	}
	if !strings.Contains(out, "pattern resolves to no package: ") {
		t.Fatalf("output missing the no-package message for the leading field; got=%q", out)
	}
}

func TestCoverageScopeCheck_RealEntriesPass(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	coverpkg := readCSIPCoverpkg(t, root)
	wantCount := strings.Count(coverpkg, ",") + 1

	exit, out := runScopeCheck(t, root, coverpkg)

	if exit != 0 {
		t.Fatalf("exit code: got %d, want 0; output=%q", exit, out)
	}
	wantMsg := "coverage-scope-check: " + strconv.Itoa(wantCount) + " patterns all resolve"
	if !strings.Contains(out, wantMsg) {
		t.Fatalf("output missing %q; got=%q", wantMsg, out)
	}
}

// coverpkgLine finds the CSIP_COVERPKG assignment in the Makefile, so the
// real-entries test always reads what `make test-csip-cover` uses, not a
// copy that can drift from it.
var coverpkgLine = regexp.MustCompile(`(?m)^CSIP_COVERPKG\s*:=\s*(.+)$`)

func readCSIPCoverpkg(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}
	m := coverpkgLine.FindSubmatch(data)
	if m == nil {
		t.Fatal("CSIP_COVERPKG not found in Makefile")
	}
	return string(m[1])
}
