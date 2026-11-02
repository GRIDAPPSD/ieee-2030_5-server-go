// Package jsonl is the one definition of the append-only JSON-lines files the
// server keeps under its data directory: how a file is read line by line, how
// a line is appended so a torn last line cannot join the next one, and how a
// file is replaced whole. Each caller owns its line format and its checksum.
package jsonl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/atomicfile"
)

// ReadLines calls keep with each non-blank line of path, in file order, and
// returns how many lines keep refused. A missing file has no lines. The line
// passed to keep is only valid during the call.
func ReadLines(path string, keep func(line []byte) bool) (dropped int, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read %q: %w", path, err)
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !keep(line) {
			dropped++
		}
	}
	return dropped, nil
}

// Encode marshals each value as one line.
func Encode[T any](vals []T) ([]byte, error) {
	var buf bytes.Buffer
	for _, v := range vals {
		line, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// Append adds payload to the file, creating it, and syncs it when sync is
// set. If the file does not end in a newline, a torn or truncated last line,
// the append starts a new line so it cannot join two records.
func Append(path string, payload []byte, sync bool) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if st, err := f.Stat(); err != nil {
		return fmt.Errorf("stat %q: %w", path, err)
	} else if st.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, st.Size()-1); err != nil {
			return fmt.Errorf("read %q: %w", path, err)
		}
		if last[0] != '\n' {
			payload = append([]byte{'\n'}, payload...)
		}
	}
	if _, err := f.Write(payload); err != nil {
		return fmt.Errorf("append %q: %w", path, err)
	}
	if sync {
		if err := f.Sync(); err != nil {
			return fmt.Errorf("sync %q: %w", path, err)
		}
	}
	return nil
}

// Sync flushes what earlier unsynced appends wrote to path. A missing file
// has nothing to flush.
func Sync(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %q: %w", path, err)
	}
	return nil
}

// Rewrite replaces path with payload atomically, or removes it when payload
// is empty.
func Rewrite(path string, payload []byte) error {
	if len(payload) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %q: %w", path, err)
		}
		return nil
	}
	return atomicfile.Write(path, payload)
}
