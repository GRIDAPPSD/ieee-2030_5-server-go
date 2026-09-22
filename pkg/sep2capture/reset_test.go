package sep2capture

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestNewStoreRefusesSymlinkedDir is acceptance 4's symlink half. The
// target directory is left empty on purpose: an empty directory would
// otherwise pass the guarded reset's own "unrecognized entry" check
// cleanly, so only the symlink check itself can be what refuses it here.
//
// Mutant (reset.go): using `os.Stat` in place of `os.Lstat` makes this
// RED, because Stat follows the symlink and reports the target's own
// (non-symlink) mode, so resetDir proceeds to write a marker into real
// instead of refusing the link.
func TestNewStoreRefusesSymlinkedDir(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatalf("Mkdir real: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}

	_, err := NewStore(StoreConfig{Dir: link})
	if !errors.Is(err, ErrDirRefused) {
		t.Fatalf("NewStore on a symlinked dir: got %v, want ErrDirRefused", err)
	}
	entries, readErr := os.ReadDir(real)
	if readErr != nil {
		t.Fatalf("ReadDir(real): %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("real directory was touched by the refused reset: got %v, want empty", entries)
	}
}

// TestNewStoreRefusesForeignFileAndSurvivesIt is acceptance 4's foreign-file
// half. Mutant (reset.go): changing the entries loop's refusal to `continue`
// instead of `return` makes this RED, because the foreign file and every
// segment sit there un-refused and the segment then gets deleted anyway.
func TestNewStoreRefusesForeignFileAndSurvivesIt(t *testing.T) {
	dir := t.TempDir()
	foreign := filepath.Join(dir, "not-ours.txt")
	if err := os.WriteFile(foreign, []byte("do not touch"), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}

	_, err := NewStore(StoreConfig{Dir: dir})
	if !errors.Is(err, ErrDirRefused) {
		t.Fatalf("NewStore on a dir with a foreign file: got %v, want ErrDirRefused", err)
	}
	data, readErr := os.ReadFile(foreign)
	if readErr != nil {
		t.Fatalf("foreign file did not survive the refused reset: %v", readErr)
	}
	if string(data) != "do not touch" {
		t.Fatalf("foreign file content changed: got %q", data)
	}
}

// TestNewStoreClearsOldSegmentsAndWritesMarker proves the reset actually
// empties a directory that is legitimately this package's own (only
// segment files and the marker), which is what makes the two refusal
// tests above meaningful rather than accidental.
func TestNewStoreClearsOldSegmentsAndWritesMarker(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "seg-000001.log")
	if err := os.WriteFile(old, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale segment: %v", err)
	}

	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	if _, statErr := os.Stat(old); !os.IsNotExist(statErr) {
		t.Fatalf("stale segment survived reset: stat err = %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(dir, capsuleMarkerName)); statErr != nil {
		t.Fatalf("marker not written: %v", statErr)
	}
	info, statErr := os.Stat(dir)
	if statErr != nil {
		t.Fatalf("stat dir: %v", statErr)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("dir mode: got %o, want 0700", info.Mode().Perm())
	}
}

// TestNewStoreCreatesMissingDir covers the first-run case: Dir does not
// exist yet (the traffic directory under a fresh DataDir).
func TestNewStoreCreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "traffic")
	st, err := NewStore(StoreConfig{Dir: dir})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { closeStore(t, st) })

	if _, statErr := os.Stat(filepath.Join(dir, capsuleMarkerName)); statErr != nil {
		t.Fatalf("marker not written in freshly created dir: %v", statErr)
	}
}
