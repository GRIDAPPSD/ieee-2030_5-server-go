package devicelog

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
)

var (
	// ErrDirUnusable means the log directory cannot be created, is not a
	// directory, or cannot be written.
	ErrDirUnusable = errors.New("device log directory unusable")

	// ErrDirProtected means the log directory is, or resolves to, a path the
	// caller listed as off limits (the certificate directory).
	ErrDirProtected = errors.New("device log directory inside a protected directory")
)

// PrepareDir creates dir if needed, checks that it can be written, and
// returns its symlink-resolved absolute path, which is the only form the
// Writer should be given. protected paths (the certificate directory and the
// key files) are compared after resolving symlinks on
// both sides, and again after creation, so neither a symlinked log
// directory nor a symlinked protected directory gets around the check. A
// refusal on a protected path creates nothing.
func PrepareDir(dir string, protected []string) (string, error) {
	if dir == "" || strings.ContainsRune(dir, 0) {
		return "", fmt.Errorf("%w: %q", ErrDirUnusable, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDirUnusable, err)
	}
	roots := make([]string, 0, len(protected))
	for _, p := range protected {
		if p == "" {
			continue
		}
		pa, err := filepath.Abs(p)
		if err != nil {
			return "", fmt.Errorf("%w: protected path %q: %w", ErrDirUnusable, p, err)
		}
		r, err := resolveLoose(pa)
		if err != nil {
			return "", fmt.Errorf("%w: protected path %q: %w", ErrDirUnusable, p, err)
		}
		roots = append(roots, r)
	}

	loose, err := resolveLoose(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDirUnusable, err)
	}
	if err := checkProtected(loose, roots); err != nil {
		return "", err
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return "", fmt.Errorf("%w: %w", ErrDirUnusable, err)
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDirUnusable, err)
	}
	if err := checkProtected(real, roots); err != nil {
		return "", err
	}
	fi, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDirUnusable, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%w: %s is not a directory", ErrDirUnusable, real)
	}
	// Anyone who can write the directory can plant or swap a log file.
	// World-writable is refused; group-writable is common on shared hosts
	// and only warned about, since the writer never follows a symlink.
	if fi.Mode().Perm()&0o002 != 0 {
		return "", fmt.Errorf("%w: %s is writable by every user", ErrDirUnusable, real)
	}
	if fi.Mode().Perm()&0o020 != 0 {
		log.Printf("device log: %s is group-writable; members of its group can replace or remove log files", logsafe.Str(real))
	}
	probe, err := os.CreateTemp(real, ".probe-*")
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrDirUnusable, err)
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		return "", fmt.Errorf("%w: %w", ErrDirUnusable, err)
	}
	if err := os.Remove(name); err != nil {
		return "", fmt.Errorf("%w: %w", ErrDirUnusable, err)
	}
	return real, nil
}

func checkProtected(path string, roots []string) error {
	for _, r := range roots {
		rel, err := filepath.Rel(r, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: %s is under %s", ErrDirProtected, path, r)
		}
	}
	return nil
}

// resolveLoose resolves symlinks in the longest existing prefix of the
// absolute path p and appends the rest unchanged, so a directory that does
// not exist yet can still be compared against one that does.
func resolveLoose(p string) (string, error) {
	var rest []string
	cur := filepath.Clean(p)
	for {
		r, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(rest) - 1; i >= 0; i-- {
				r = filepath.Join(r, rest[i])
			}
			return r, nil
		}
		if !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = append(rest, filepath.Base(cur))
		cur = parent
	}
}
