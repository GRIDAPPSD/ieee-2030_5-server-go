// #193 — Coverage-gate script unit tests.
//
// The gate is a shell script (scripts/coverage-gate.sh); we test it
// from Go because the rest of the test suite is `go test` and CI
// already has the toolchain on the runner. Each test writes a
// fixture coverage profile to a temp dir, runs the script, and
// checks exit code + stdout against the expected gate decision.
//
// Fixture profiles are minimal valid `go tool cover` inputs. The
// `mode:` header is required; each subsequent line is
// `file:start.col,end.col stmts count` per the Go coverage profile
// format.
package coveragegate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// scriptPath returns an absolute path to scripts/coverage-gate.sh.
// The test runs from this file's directory under `go test`, so the
// script is in the same directory.
func scriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "coverage-gate.sh")
}

// writeProfile emits a synthetic coverage profile achieving an
// integer covered/total ratio. The gate parses statements (not
// lines) directly from the profile rows, so we encode the desired
// percentage as `covered=pct, uncovered=100-pct` with one statement
// per row.
func writeProfile(t *testing.T, dir string, pctCovered int) string {
	t.Helper()
	if pctCovered < 0 || pctCovered > 100 {
		t.Fatalf("pctCovered out of range: %d", pctCovered)
	}

	var sb strings.Builder
	sb.WriteString("mode: set\n")
	// Each row format: "<file>:<startLine>.<startCol>,<endLine>.<endCol> <stmts> <count>"
	// Distinct ranges per row so the cover tool counts them all.
	for i := 0; i < 100; i++ {
		count := 0
		if i < pctCovered {
			count = 1
		}
		// Use distinct end columns to keep ranges unique.
		startLine := i + 1
		endLine := startLine + 1
		row := "github.com/GRIDAPPSD/ieee-2030_5-server-go/_synth.go:" +
			strconv.Itoa(startLine) + ".1," + strconv.Itoa(endLine) + ".2 1 " + strconv.Itoa(count) + "\n"
		sb.WriteString(row)
	}

	path := filepath.Join(dir, "coverage.out")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return path
}

func TestCoverageGate(t *testing.T) {
	t.Parallel()

	script := scriptPath(t)

	cases := []struct {
		name      string
		coverage  int
		threshold string
		wantExit  int
		wantInOut string
	}{
		{
			name:      "above_threshold_passes",
			coverage:  85,
			threshold: "78",
			wantExit:  0,
			wantInOut: "result=PASS",
		},
		{
			name:      "exactly_at_threshold_passes",
			coverage:  78,
			threshold: "78",
			wantExit:  0,
			wantInOut: "result=PASS",
		},
		{
			name:      "below_threshold_fails",
			coverage:  77,
			threshold: "78",
			wantExit:  1,
			wantInOut: "result=FAIL",
		},
		{
			name:      "far_below_threshold_fails",
			coverage:  50,
			threshold: "78",
			wantExit:  1,
			wantInOut: "result=FAIL",
		},
		{
			name:      "default_threshold_applied_when_omitted",
			coverage:  79,
			threshold: "", // omit → script defaults to 78
			wantExit:  0,
			wantInOut: "threshold=78%",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			profile := writeProfile(t, dir, tc.coverage)

			args := []string{profile}
			if tc.threshold != "" {
				args = append(args, tc.threshold)
			}
			cmd := exec.Command(script, args...)
			out, err := cmd.CombinedOutput()
			exit := 0
			if err != nil {
				if ee, ok := err.(*exec.ExitError); ok {
					exit = ee.ExitCode()
				} else {
					t.Fatalf("exec failed (not ExitError): %v; output=%q", err, string(out))
				}
			}
			if exit != tc.wantExit {
				t.Fatalf("exit code: got %d, want %d; output=%q", exit, tc.wantExit, string(out))
			}
			if !strings.Contains(string(out), tc.wantInOut) {
				t.Fatalf("output missing %q; got=%q", tc.wantInOut, string(out))
			}
		})
	}
}

func TestCoverageGate_MissingProfile(t *testing.T) {
	t.Parallel()
	script := scriptPath(t)
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.out")
	cmd := exec.Command(script, missing)
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T (%v); output=%q", err, err, string(out))
	}
	if ee.ExitCode() != 2 {
		t.Fatalf("exit code: got %d, want 2; output=%q", ee.ExitCode(), string(out))
	}
	if !strings.Contains(string(out), "profile not found") {
		t.Fatalf("output missing 'profile not found'; got=%q", string(out))
	}
}

func TestCoverageGate_InvalidThreshold(t *testing.T) {
	t.Parallel()
	script := scriptPath(t)
	dir := t.TempDir()
	profile := writeProfile(t, dir, 80)
	cmd := exec.Command(script, profile, "not-a-number")
	out, err := cmd.CombinedOutput()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected ExitError, got %T; output=%q", err, string(out))
	}
	if ee.ExitCode() != 2 {
		t.Fatalf("exit code: got %d, want 2; output=%q", ee.ExitCode(), string(out))
	}
}
