// Package atomicfile replaces a file's contents so that a crash leaves either
// the old contents or the new ones, never a truncated file.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces path with payload through a synced sibling <path>.tmp and a
// rename. The file is created with mode 0o600 because every caller writes
// operator-sensitive state. O_TRUNC overwrites a stale temporary file left by
// an earlier crash. The directory sync after the rename is best effort: the
// contents are already synced, and some platforms cannot sync a directory.
func Write(path string, payload []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open tmp: %w", err)
	}
	// After a successful rename the temporary file is gone and Remove fails
	// with not-exist, which is the expected outcome.
	defer func() { _ = os.Remove(tmp) }()

	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
