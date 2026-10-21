package dercontrol

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// countInSources counts occurrences of needle across the package's non-test
// Go files.
func countInSources(t *testing.T, dir, needle string) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob: %v (%d files)", err, len(files))
	}
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		n += strings.Count(string(b), needle)
	}
	return n
}

// TestOneWritePath pins writeCopies as the only place a control or its
// lifecycle record is created, and intervalEnd as the only place an interval
// end is computed, so a second sequence cannot reappear unnoticed.
func TestOneWritePath(t *testing.T) {
	for _, tc := range []struct {
		needle string
		want   int
	}{
		{"i.lifecycles.Create(", 1},
		{"i.controls.Create(", 1},
		{"Interval.Start + int64(", 0},
		{"iv.Start + int64(", 1},
	} {
		if got := countInSources(t, ".", tc.needle); got != tc.want {
			t.Errorf("%q appears %d times in non-test sources, want %d", tc.needle, got, tc.want)
		}
	}
}
