package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// Durable EndDeviceStore persistence (GRIDAPPSD/ieee-2030_5-server-go#165).
//
// Wraps the existing in-memory EndDeviceStore with on-disk JSON snapshots
// using the shared envelope/atomic-write machinery in persistence.go. The
// underlying *EndDeviceStore stays unchanged so every existing test path
// that builds a NewEndDeviceStore() keeps the same in-memory semantics:
// persistence is composed via a constructor wrapper, not an interface
// change (Pike rule: don't widen public surface to add a feature).

// endDeviceRecord is the on-disk shape: store key + the EndDevice value.
// The key is what the in-memory store indexes by; SFDI/LFDI secondary
// indexes are rebuilt from the device fields on load.
type endDeviceRecord struct {
	ID     string         `json:"id"`
	Device sep2.EndDevice `json:"device"`
}

// persistentEndDeviceWrapper is internal bookkeeping attached to an
// EndDeviceStore when a persistence path is configured. We keep it out of
// the base struct so the zero-value EndDeviceStore stays exactly the same
// shape it had before persistence existed (no extra fields shipped on
// every in-memory user).
type persistentEndDeviceWrapper struct {
	mu   sync.Mutex
	path string
}

// endDevicePersist maps a configured store to its persistence wrapper.
// Keyed by *EndDeviceStore (pointer identity) so multiple persistent
// stores in the same process don't interfere.
var (
	endDevicePersistMu sync.RWMutex
	endDevicePersist   = make(map[*EndDeviceStore]*persistentEndDeviceWrapper)
)

// NewEndDeviceStoreWithPersistence builds an EndDeviceStore wired to a
// JSON file on disk. An empty path means "in-memory only": equivalent to
// NewEndDeviceStore, and returns a non-nil store.
//
// If the file exists it is loaded; a missing file is cold boot (no
// error). Corrupt or unknown-version files return an error and the caller
// decides whether to rebuild or fail.
func NewEndDeviceStoreWithPersistence(path string) (*EndDeviceStore, error) {
	store := NewEndDeviceStore()
	if path == "" {
		return store, nil
	}
	if err := store.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("enddevice persistence: load %q: %w", path, err)
	}
	wrapper := &persistentEndDeviceWrapper{path: path}
	endDevicePersistMu.Lock()
	endDevicePersist[store] = wrapper
	endDevicePersistMu.Unlock()
	return store, nil
}

// loadFromFile rehydrates the store from the given path. Missing file is
// cold boot (no error, no state change). Corrupt or unknown-version files
// return an error.
func (s *EndDeviceStore) loadFromFile(path string) error {
	env, err := readSnapshotEnvelope(path)
	if err != nil {
		return err
	}
	if env == nil {
		return nil // cold boot
	}
	if len(env.Records) == 0 {
		return nil // empty records
	}
	var records []endDeviceRecord
	if err := json.Unmarshal(env.Records, &records); err != nil {
		return fmt.Errorf("decode records: %w", err)
	}

	ctx := context.Background()
	for _, r := range records {
		// Re-use the wrapped Create so secondary indexes (SFDI/LFDI) are
		// populated through the same code path used at runtime.
		if err := s.Store.Create(ctx, r.ID, r.Device); err != nil {
			return fmt.Errorf("rehydrate %q: %w", r.ID, err)
		}
		s.indexDevice(r.ID, r.Device)
	}
	return nil
}

// snapshotEndDevices captures the current set of (id, device) pairs under
// the underlying Store's read lock so the on-disk snapshot is internally
// consistent even if a writer is mid-flight on another goroutine.
func (s *EndDeviceStore) snapshotEndDevices() []endDeviceRecord {
	s.Store.mu.RLock()
	defer s.Store.mu.RUnlock()

	records := make([]endDeviceRecord, 0, len(s.Store.keys))
	for _, k := range s.Store.keys {
		records = append(records, endDeviceRecord{
			ID:     k,
			Device: s.Store.data[k].Copy(),
		})
	}
	return records
}

// persistEndDeviceSnapshot writes the current store contents to disk if a
// persistence path is configured. No-op for in-memory stores. Returns nil
// without disk I/O when no path is wired.
func (s *EndDeviceStore) persistEndDeviceSnapshot() error {
	endDevicePersistMu.RLock()
	wrapper, ok := endDevicePersist[s]
	endDevicePersistMu.RUnlock()
	if !ok {
		return nil
	}
	wrapper.mu.Lock()
	defer wrapper.mu.Unlock()

	records := s.snapshotEndDevices()
	if err := writeSnapshotEnvelope(wrapper.path, records); err != nil {
		return fmt.Errorf("enddevice persistence: %w", err)
	}
	return nil
}
