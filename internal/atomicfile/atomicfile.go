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
	s, err := Stage(path, ".tmp", payload)
	if err != nil {
		return err
	}
	return s.Commit(nil)
}

// Staged is a replacement for a file, written and synced beside it so a
// caller can do the slow part before taking a lock. Commit adds a short tail
// and renames it over the file; Abort discards it.
type Staged struct {
	path, tmp string
	f         *os.File
}

// Stage writes payload to <path><suffix> and syncs it. Write uses ".tmp"; a
// caller staging while Write may run on the same path names another suffix.
// On an error the temporary file is removed.
func Stage(path, suffix string, payload []byte) (*Staged, error) {
	tmp := path + suffix
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open tmp: %w", err)
	}
	s := &Staged{path: path, tmp: tmp, f: f}
	if err := s.write(payload); err != nil {
		return nil, err
	}
	return s, nil
}

// write appends b and syncs; on an error the temporary file is removed.
func (s *Staged) write(b []byte) error {
	if _, err := s.f.Write(b); err != nil {
		s.Abort()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := s.f.Sync(); err != nil {
		s.Abort()
		return fmt.Errorf("fsync tmp: %w", err)
	}
	return nil
}

// Commit appends tail, syncs it, and renames the staged file over path. On an
// error the temporary file is removed and path is left as it was.
func (s *Staged) Commit(tail []byte) error {
	if len(tail) > 0 {
		if err := s.write(tail); err != nil {
			return err
		}
	}
	// After a successful rename the temporary file is gone and Remove fails
	// with not-exist, which is the expected outcome.
	defer func() { _ = os.Remove(s.tmp) }()
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(s.tmp, s.path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	if d, err := os.Open(filepath.Dir(s.path)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Abort closes and removes the staged file.
func (s *Staged) Abort() {
	_ = s.f.Close()
	_ = os.Remove(s.tmp)
}
