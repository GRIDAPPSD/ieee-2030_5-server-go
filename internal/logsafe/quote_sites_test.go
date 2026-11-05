package logsafe

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A log message must not wrap a value in a hand-written \"%s\": a quote in the
// value would close the field. Quote is the one way to write a quoted field.
func TestNoHandQuotedLogFields(t *testing.T) {
	var scanned int
	for _, dir := range []string{"cmd", "internal", "pkg"} {
		err := filepath.WalkDir(filepath.Join(moduleRoot, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			for i, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, `\"%s\"`) {
					t.Errorf("%s:%d: wrap the value with logsafe.Quote instead of \\\"%%s\\\"", path, i+1)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d files; the walk is not reaching the tree", scanned)
	}
}
