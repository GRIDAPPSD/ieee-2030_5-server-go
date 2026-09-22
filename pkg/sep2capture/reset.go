package sep2capture

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// capsuleMarkerName is the file a guarded reset writes to prove a
// directory is this package's own: on the next start, its presence is
// part of what makes the directory safe to empty rather than refuse.
const capsuleMarkerName = ".sep2capture"

// ErrDirRefused is returned by NewStore when Dir fails the guarded reset:
// it is a symlink, or it holds an entry that is neither a segment file nor
// this package's own marker. Capture is off either way, and resetDir has
// deleted nothing under Dir.
var ErrDirRefused = errors.New("sep2capture: capture directory refused")

// isSegmentFileName reports whether name is exactly the shape
// (*Store).segmentPath produces: "seg-" then one or more digits then
// ".log". Nothing else in Dir survives a guarded reset's own deletion
// pass, and nothing else lets the reset proceed at all (an unrecognized
// entry refuses the whole reset, per the loop in resetDir).
func isSegmentFileName(name string) bool {
	const prefix, suffix = "seg-", ".log"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return false
	}
	mid := name[len(prefix) : len(name)-len(suffix)]
	if mid == "" {
		return false
	}
	for _, r := range mid {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// resetDir empties dir of every seg-*.log file and writes a fresh marker,
// so a listener never accepts while an old process's segments are still
// there. It checks dir itself, not just its contents: a symlinked dir
// could point anywhere an operator's directory variable happens to
// resolve, and data-invariants rule 3 treats the boundary itself as a
// candidate for the disallowed condition, not only what is under it. A
// foreign file is the other refusal: it is evidence dir was never meant
// for this package, and deleting around it would be a guess. Both
// refusals leave dir exactly as found.
func resetDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("sep2capture: create %s: %w", dir, err)
		}
	case err != nil:
		return fmt.Errorf("sep2capture: stat %s: %w", dir, err)
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%w: %s is a symlink", ErrDirRefused, dir)
	case !info.IsDir():
		return fmt.Errorf("%w: %s is not a directory", ErrDirRefused, dir)
	default:
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("sep2capture: read %s: %w", dir, err)
		}
		for _, e := range entries {
			if e.Name() == capsuleMarkerName || isSegmentFileName(e.Name()) {
				continue
			}
			return fmt.Errorf("%w: %s holds an unrecognized entry %q", ErrDirRefused, dir, e.Name())
		}
		for _, e := range entries {
			if !isSegmentFileName(e.Name()) {
				continue
			}
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return fmt.Errorf("sep2capture: remove %s: %w", e.Name(), err)
			}
		}
	}

	// Every path above either created a fresh, empty dir or validated and
	// cleared an existing one; both need the same mode and marker below.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("sep2capture: chmod %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, capsuleMarkerName), nil, 0o600); err != nil {
		return fmt.Errorf("sep2capture: write marker: %w", err)
	}
	return nil
}
