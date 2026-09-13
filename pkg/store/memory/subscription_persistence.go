package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/atomicfile"
)

// Path B: durable subscription persistence
// (GRIDAPPSD/ieee-2030_5-server-go#224).
//
// Path A (GRIDAPPSD/ieee-2030_5-server-go#25) is a test-only
// SnapshotForTesting / RestoreForTesting pair that lets the CSIP harness
// simulate a server power-reset entirely in memory.
// Path A is sufficient for the harness; it is NOT sufficient for any
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
// The record element re-uses SubscriptionRecord from Path A: same wire
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
// = cold boot (no error). Any decode error is fatal: callers must decide
// whether to rebuild the file or fail.
//
// All subsequent Create / Delete operations flush a fresh snapshot to the
// same path under an atomic-rename. Reads remain pure in-memory.
//
// Pass an empty path to mean "in-memory only": equivalent to
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
// LoadFromFile does NOT configure on-going persistence: call it once at
// startup; future writes go to whatever persistPath the store was built
// with (or nowhere, if the store is pure in-memory).
func (s *SubscriptionStore) LoadFromFile(path string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Cold boot: no prior snapshot. This is normal on first run.
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
	// hook uses: gives us secondary-index rebuilds for free.
	s.RestoreForTesting(snap.Records)
	return nil
}

// persistSnapshot flushes a fresh JSON snapshot to disk under an atomic
// rename. No-op when persistPath is empty (in-memory mode). The snapshot is
// marshalled under the store's read lock for a consistent point-in-time view,
// then written through atomicfile.Write, so a crash leaves the previously
// committed <path> intact.
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

	if err := atomicfile.Write(s.persistPath, payload); err != nil {
		return fmt.Errorf("subscription persistence: %w", err)
	}
	return nil
}
