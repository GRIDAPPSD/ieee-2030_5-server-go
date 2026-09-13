package atomicfile_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/atomicfile"
)

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return raw
}

func assertNoTemp(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("temporary file after Write: stat err = %v, want not exist", err)
	}
}

func TestWriteCreatesFileWithOwnerOnlyMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")

	if err := atomicfile.Write(path, []byte(`{"version":1}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := readFile(t, path); string(got) != `{"version":1}` {
		t.Errorf("contents = %q, want the payload", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600", mode)
	}
	assertNoTemp(t, path)
}

func TestWriteReplacesContentsOverStaleTemp(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old contents that are longer"), 0o600); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	if err := os.WriteFile(path+".tmp", []byte("{partial garbage from a crash"), 0o600); err != nil {
		t.Fatalf("write stale temp: %v", err)
	}

	if err := atomicfile.Write(path, []byte("new")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := readFile(t, path); string(got) != "new" {
		t.Errorf("contents = %q, want exactly the new payload", got)
	}
	assertNoTemp(t, path)
}

func TestWriteFailureLeavesCommittedFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.json")
	committed := []byte("committed")
	if err := os.WriteFile(path, committed, 0o600); err != nil {
		t.Fatalf("write committed file: %v", err)
	}
	// A non-empty directory at the temporary path makes the open fail.
	if err := os.MkdirAll(filepath.Join(path+".tmp", "keep"), 0o700); err != nil {
		t.Fatalf("create blocker: %v", err)
	}

	err := atomicfile.Write(path, []byte("never lands"))
	if err == nil {
		t.Fatal("Write succeeded with a directory at the temporary path, want an error")
	}
	if !strings.HasPrefix(err.Error(), "open tmp: ") {
		t.Errorf("error = %q, want the open tmp step named", err)
	}
	if got := readFile(t, path); !bytes.Equal(got, committed) {
		t.Errorf("committed file = %q, want it unchanged", got)
	}
}
