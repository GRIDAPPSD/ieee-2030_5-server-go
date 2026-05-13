package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// IEEE-077 — Path B durable subscription persistence.
//
// IEEE-022 shipped Path A: a test-only SnapshotForTesting / RestoreForTesting
// pair that lets the CSIP harness simulate a server power-reset entirely in
// memory. Path A is sufficient for the harness; it is NOT sufficient for any
// deployment claim that "subscriptions survive a real power loss."
//
// This file ships Path B: when a path is configured the SubscriptionStore
// writes a fresh JSON snapshot to disk on every Create / Delete, using
// write-temp-then-atomic-rename so a crash mid-write cannot corrupt the
// previously committed file. On startup the server calls LoadFromFile to
// rehydrate.
//
// On-disk format (versioned so future migrations stay forwards-compatible):
//
//	{
//	  "version": 1,
//	  "records": [
//	    {"id": "sub-1", "subscription": { ... }},
//	    ...
//	  ]
//	}
//
// The record element re-uses SubscriptionRecord from IEEE-022 — same wire
// shape we already snapshot/restore through.

// persistenceVersion is the current on-disk schema version. Bump on a
// schema-breaking change; readers tolerate older versions but reject
// versions they don't understand.
const persistenceVersion = 1

// persistedSnapshot is the root JSON document written to disk.
type persistedSnapshot struct {
	Version int                  `json:"version"`
	Records []SubscriptionRecord `json:"records"`
}

// NewSubscriptionStoreWithPersistence builds a SubscriptionStore wired to a
// JSON file on disk. If the file already exists it is loaded; missing file
// = cold boot (no error). Any decode error is fatal — callers must decide
// whether to rebuild the file or fail.
//
// All subsequent Create / Delete operations flush a fresh snapshot to the
// same path under an atomic-rename. Reads remain pure in-memory.
//
// Pass an empty path to mean "in-memory only" — equivalent to
// NewSubscriptionStore. We still return a non-nil store in that case.
func NewSubscriptionStoreWithPersistence(path string) (*SubscriptionStore, error) {
	store := NewSubscriptionStore()
	if path == "" {
		return store, nil
	}
	if err := store.LoadFromFile(path); err != nil {
		return nil, fmt.Errorf("subscription persistence: load %q: %w", path, err)
	}
	store.persistPath = path
	return store, nil
}

// LoadFromFile rehydrates the store from the given JSON file. A missing
// file is treated as cold boot (no error, no state change). A corrupt or
// version-unknown file returns an error and leaves the store untouched.
//
// LoadFromFile does NOT configure on-going persistence — call it once at
// startup; future writes go to whatever persistPath the store was built
// with (or nowhere, if the store is pure in-memory).
func (s *SubscriptionStore) LoadFromFile(path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Cold boot — no prior snapshot. This is normal on first run.
			return nil
		}
		return fmt.Errorf("read %q: %w", path, err)
	}
	if len(data) == 0 {
		// Empty file (e.g. the previous write was zero records) is a
		// valid "no subscriptions" state.
		return nil
	}
	var snap persistedSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("decode %q: %w", path, err)
	}
	if snap.Version != persistenceVersion {
		return fmt.Errorf("unsupported subscription snapshot version %d (want %d)",
			snap.Version, persistenceVersion)
	}

	// Replace the in-memory state via the same restore path the test
	// hook uses — gives us secondary-index rebuilds for free.
	s.RestoreForTesting(snap.Records)
	return nil
}

// persistSnapshot flushes a fresh JSON snapshot to disk under an atomic
// rename. No-op when persistPath is empty (in-memory mode).
//
// The atomic-write recipe:
//  1. Marshal the current snapshot under the store's read lock so we
//     capture a consistent point-in-time view.
//  2. Write to <path>.tmp; fsync the tmp file.
//  3. os.Rename(<path>.tmp, <path>).
//  4. Best-effort fsync the parent directory so the rename is durable.
//
// A crash between steps 2 and 3 leaves the previously committed <path>
// intact; the stale .tmp is overwritten on the next successful write.
func (s *SubscriptionStore) persistSnapshot() error {
	if s.persistPath == "" {
		return nil
	}
	// Serialize writers against each other so a slower writer cannot
	// rename an older snapshot over a newer one.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	// Snapshot under the underlying store's read lock for a consistent
	// view. SnapshotForTesting already does this.
	records := s.SnapshotForTesting()

	doc := persistedSnapshot{
		Version: persistenceVersion,
		Records: records,
	}
	payload, err := json.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("subscription persistence: marshal: %w", err)
	}

	return writeFileAtomic(s.persistPath, payload)
}

// writeFileAtomic writes payload to path via a sibling .tmp file and
// os.Rename. Best-effort directory fsync improves durability without
// being load-bearing on platforms where Sync is a no-op.
func writeFileAtomic(path string, payload []byte) error {
	dir := filepath.Dir(path)
	tmp := path + ".tmp"

	// O_TRUNC so a stale .tmp from a prior crashed write is overwritten,
	// not appended to. 0o600 — subscription state is operator-sensitive
	// (notification URIs, device hrefs).
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("subscription persistence: open tmp: %w", err)
	}
	// On any error path below, remove the tmp file so we don't leak it.
	defer func() {
		// If rename succeeded the tmp file no longer exists; Remove
		// will return ENOENT, which we ignore.
		_ = os.Remove(tmp)
	}()

	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		return fmt.Errorf("subscription persistence: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("subscription persistence: fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("subscription persistence: close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("subscription persistence: rename: %w", err)
	}

	// Directory fsync: ensures the rename is durable on POSIX
	// filesystems. Errors here are non-fatal — the data is already
	// on disk via the file fsync; this just hardens the directory
	// entry. We swallow ENOTSUP / EISDIR-on-Windows quietly.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

