//go:build windows

package devicelog

import (
	"path/filepath"
	"testing"
)

// Go reports every Windows directory as 0777, so the unix world-writable
// check must not run: the directory is accepted and usable.
func TestPrepareDirAcceptsWindowsDirectory(t *testing.T) {
	dir := filepath.Join(evalDir(t, t.TempDir()), "logs")
	got, err := PrepareDir(dir, nil)
	if err != nil {
		t.Fatalf("PrepareDir(%q) = %v; want the directory accepted on Windows", dir, err)
	}
	if got != dir {
		t.Errorf("PrepareDir = %q; want %q", got, dir)
	}
}
