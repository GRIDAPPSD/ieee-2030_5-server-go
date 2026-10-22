package devicelog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	readChunk = 64 << 10

	// openAttempts bounds the retries of openLogFile when the file at the
	// path is replaced between its Lstat and its open, which a rotation does.
	openAttempts = 3
)

// logPaths is the one place a file key becomes file names: the current file
// and the rotated one. The name is .1.jsonl, not .jsonl.1, so the
// repository's ignore of *.jsonl covers it.
func logPaths(dir, key string) (cur, old string) {
	return filepath.Join(dir, key+".jsonl"), filepath.Join(dir, key+".1.jsonl")
}

// afterLstat runs between openLogFile's Lstat and open; tests use it to
// rotate the file at that moment.
var afterLstat func(path string)

// openLogFile is the one way the package opens a log file. It never follows
// a symlink and returns only a regular file. A path that is replaced between
// the Lstat and the open, as a rotation does, is retried; a symlink is
// refused with ErrNotRegular. flag may create the file, in which case a
// path that does not exist yet is opened as is.
func openLogFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	for range openAttempts {
		lst, lerr := os.Lstat(path)
		switch {
		case lerr == nil && !lst.Mode().IsRegular():
			return nil, fmt.Errorf("%w: %s", ErrNotRegular, filepath.Base(path))
		case lerr != nil && (!errors.Is(lerr, os.ErrNotExist) || flag&os.O_CREATE == 0):
			return nil, lerr
		}
		if afterLstat != nil {
			afterLstat(path)
		}
		f, err := os.OpenFile(path, flag|noFollow, perm)
		if err != nil {
			if openedSymlink(err) {
				return nil, fmt.Errorf("%w: %s", ErrNotRegular, filepath.Base(path))
			}
			return nil, err
		}
		fi, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		if !fi.Mode().IsRegular() {
			_ = f.Close()
			return nil, fmt.Errorf("%w: %s", ErrNotRegular, filepath.Base(path))
		}
		if lerr == nil && !os.SameFile(lst, fi) {
			_ = f.Close()
			continue
		}
		return f, nil
	}
	return nil, fmt.Errorf("%w: %s kept changing while opening", ErrNotRegular, filepath.Base(path))
}

// openForRead opens a log file read-only and returns its size now. The size
// bounds the read so a line still being appended is never half-read.
func openForRead(path string) (*os.File, int64, error) {
	f, err := openLogFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

// eachLineBackward is the one backward line scanner: it calls fn for each
// complete line of the first size bytes of f, last line first, until fn
// returns false. fn must copy a line it keeps. A line longer than
// MaxLineBytes, which the writer never produces, is skipped. A final line
// still being written has no newline and is cut short, so a JSON parse in fn
// refuses it.
func eachLineBackward(f *os.File, size int64, fn func(line []byte) bool) error {
	var carry []byte
	// dropTail discards the last row of the next chunk: the head of a line
	// too long to keep.
	dropTail := false
	pos := size
	for pos > 0 {
		start := max(0, pos-readChunk)
		buf := make([]byte, pos-start, int(pos-start)+len(carry))
		if _, err := f.ReadAt(buf, start); err != nil {
			return err
		}
		data := append(buf, carry...)
		carry = nil
		rows := bytes.Split(data, []byte{'\n'})
		if dropTail {
			rows = rows[:len(rows)-1]
			dropTail = len(rows) == 0
		}
		if start > 0 && len(rows) > 0 {
			carry, rows = rows[0], rows[1:]
			if len(carry) > MaxLineBytes {
				carry, dropTail = nil, true
			}
		}
		for i := len(rows) - 1; i >= 0; i-- {
			if len(rows[i]) == 0 || len(rows[i]) > MaxLineBytes {
				continue
			}
			if !fn(rows[i]) {
				return nil
			}
		}
		pos = start
	}
	return nil
}
