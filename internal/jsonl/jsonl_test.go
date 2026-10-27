package jsonl

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// An append after a torn last line starts a new line, ReadLines reports the
// torn one as refused, and Rewrite with nothing removes the file.
func TestAppendAfterATornLineAndRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	if err := os.WriteFile(path, []byte("{\"a\":1}\n{\"a\":"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, err := Encode([]map[string]int{{"a": 2}, {"a": 3}})
	if err != nil {
		t.Fatal(err)
	}
	if err := Append(path, payload, false); err != nil {
		t.Fatal(err)
	}
	if err := Sync(path); err != nil {
		t.Fatal(err)
	}
	var kept []string
	dropped, err := ReadLines(path, func(line []byte) bool {
		if line[len(line)-1] != '}' {
			return false
		}
		kept = append(kept, string(line))
		return true
	})
	if err != nil || dropped != 1 || !slices.Equal(kept, []string{`{"a":1}`, `{"a":2}`, `{"a":3}`}) {
		t.Fatalf("ReadLines = %q, %d dropped, %v; want three lines and the torn one dropped", kept, dropped, err)
	}
	if err := Rewrite(path, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file after an empty rewrite: %v, want it removed", err)
	}
	if n, err := ReadLines(path, func([]byte) bool { return false }); n != 0 || err != nil {
		t.Errorf("ReadLines of a missing file = %d, %v; want 0 and nil", n, err)
	}
	if err := Sync(path); err != nil {
		t.Errorf("Sync of a missing file = %v, want nil", err)
	}
}
