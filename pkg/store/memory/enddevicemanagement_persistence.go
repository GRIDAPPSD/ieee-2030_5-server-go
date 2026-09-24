package memory

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Durable EndDeviceManagementStore persistence
// (GRIDAPPSD/ieee-2030_5-server-go#440), following the shared
// envelope/atomic-write machinery in persistence.go and the same
// wrapper shape as the other five admin-mutated stores.

// managementPairRecord is the on-disk shape of one (manager, managed) pair.
type managementPairRecord struct {
	ManagerLFDI string `json:"managerLFDI"`
	ManagedLFDI string `json:"managedLFDI"`
}

// NewEndDeviceManagementStoreWithPersistence builds an
// EndDeviceManagementStore wired to a JSON snapshot file. An empty path
// means "in-memory only": equivalent to NewEndDeviceManagementStore.
//
// If the file exists it is loaded; a missing file is cold boot (no error).
// Corrupt or unknown-version files return an error and the caller decides
// whether to rebuild or fail.
func NewEndDeviceManagementStoreWithPersistence(path string) (*EndDeviceManagementStore, error) {
	s := NewEndDeviceManagementStore()
	if path == "" {
		return s, nil
	}
	if err := s.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("enddevice management persistence: load %q: %w", path, err)
	}
	s.persistPath = path
	return s, nil
}

// loadFromFile rehydrates the store from the given path. Missing file is
// cold boot (no error, no state change). Corrupt or unknown-version files
// return an error.
func (s *EndDeviceManagementStore) loadFromFile(path string) error {
	env, err := readSnapshotEnvelope(path)
	if err != nil {
		return err
	}
	if env == nil || len(env.Records) == 0 {
		return nil
	}
	var records []managementPairRecord
	if err := json.Unmarshal(env.Records, &records); err != nil {
		return fmt.Errorf("decode records: %w", err)
	}

	// Rehydrate directly into the maps rather than through Assign: the
	// records already came from a store that enforced one manager per
	// device, and we are pre-publication so there is no lock contention to
	// serialize against.
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range records {
		s.managerOf[r.ManagedLFDI] = r.ManagerLFDI
		if s.managedBy[r.ManagerLFDI] == nil {
			s.managedBy[r.ManagerLFDI] = make(map[string]struct{})
		}
		s.managedBy[r.ManagerLFDI][r.ManagedLFDI] = struct{}{}
	}
	return nil
}

// snapshotRecords captures the current pairs, sorted by managed LFDI for a
// deterministic on-disk order, under the store's read lock.
func (s *EndDeviceManagementStore) snapshotRecords() []managementPairRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]managementPairRecord, 0, len(s.managerOf))
	for managed, manager := range s.managerOf {
		out = append(out, managementPairRecord{ManagerLFDI: manager, ManagedLFDI: managed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ManagedLFDI < out[j].ManagedLFDI })
	return out
}

// persist flushes the snapshot to disk if persistence is configured. No-op
// for in-memory stores.
func (s *EndDeviceManagementStore) persist() error {
	if s.persistPath == "" {
		return nil
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	records := s.snapshotRecords()
	if err := writeSnapshotEnvelope(s.persistPath, records); err != nil {
		return fmt.Errorf("enddevice management persistence: %w", err)
	}
	return nil
}
