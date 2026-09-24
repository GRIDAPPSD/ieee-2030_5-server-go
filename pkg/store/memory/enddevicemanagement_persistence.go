package memory

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Durable EndDeviceManagementStore persistence
// (GRIDAPPSD/ieee-2030_5-server-go#440), following the shared
// envelope/atomic-write machinery in persistence.go.
//
// This wrapper diverges from the other five admin-mutated stores in one
// deliberate way (GRIDAPPSD/ieee-2030_5-server-go#677 fix round): the
// candidate snapshot is written to disk before the corresponding change is
// applied in memory, under EndDeviceManagementStore.persistMu rather than
// EndDeviceManagementStore.mu, and outside mu entirely. See the persistPath
// and persistMu field comments on EndDeviceManagementStore for why: a
// management pair grants protocol access, so it must never be live before it
// is durable, and a disk write in progress must never hold out a reader
// (GET /edev, the protocol-side ownership gate) that has no relationship to
// the write.

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
// A corrupt file, an unsupported version, or a record that fails the same
// checks Assign enforces (canonical LFDI, no self-management, one manager
// per device) returns an error and the caller decides whether to rebuild or
// fail: those rules gate a pair on every path it can enter the store, not
// only the admin plane.
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
// cold boot (no error, no state change). A corrupt file, an unsupported
// version, or a record that fails validation returns an error and leaves
// the store untouched: a file that fails partway is refused wholesale
// rather than loaded with the bad records silently dropped, so a caller
// that chooses to carry on does so having decided that, not by accident.
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

	managerOf := make(map[string]string, len(records))
	managedBy := make(map[string]map[string]struct{}, len(records))
	for _, r := range records {
		if err := checkCanonicalLFDI("manager", r.ManagerLFDI); err != nil {
			return fmt.Errorf("record (manager %q, managed %q): %w", r.ManagerLFDI, r.ManagedLFDI, err)
		}
		if err := checkCanonicalLFDI("managed", r.ManagedLFDI); err != nil {
			return fmt.Errorf("record (manager %q, managed %q): %w", r.ManagerLFDI, r.ManagedLFDI, err)
		}
		if r.ManagerLFDI == r.ManagedLFDI {
			return fmt.Errorf("record: %s cannot manage itself", r.ManagedLFDI)
		}
		if _, dup := managerOf[r.ManagedLFDI]; dup {
			return fmt.Errorf("record: %s has more than one manager on disk", r.ManagedLFDI)
		}
		managerOf[r.ManagedLFDI] = r.ManagerLFDI
		if managedBy[r.ManagerLFDI] == nil {
			managedBy[r.ManagerLFDI] = make(map[string]struct{})
		}
		managedBy[r.ManagerLFDI][r.ManagedLFDI] = struct{}{}
	}

	// Swap in the validated maps atomically: a record failing partway
	// through the loop above must never leave the store holding a prefix
	// of the file's records. Also takes persistMu (#677 fix round item 6),
	// the same lock every mutate-routed write holds, even though load never
	// calls persistRecords: it is unreachable concurrently today (the only
	// caller is the constructor, before the store is shared), but a reload
	// path added later must not be the one write site that skips the
	// convention every other write to these maps holds.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.managerOf = managerOf
	s.managedBy = managedBy
	return nil
}

// recordsLocked captures the current pairs as a freshly built, independent
// slice, sorted by managed LFDI at the moment it is built. The caller must
// already hold s.mu (for read or write); recordsLocked takes no lock of its
// own, so a mutating method can call it under either to build the candidate
// snapshot for the change it is about to make. A caller that appends a new
// record or rewrites a record's ManagedLFDI field afterward (Assign,
// RekeyManaged) does not get a re-sorted slice back: persistRecords sorts
// again immediately before writing (#677 fix round item 6), so the on-disk
// order is always sorted regardless of how the candidate slice was built,
// matching this comment's own promise about the file rather than about this
// function's return value alone.
func (s *EndDeviceManagementStore) recordsLocked() []managementPairRecord {
	out := make([]managementPairRecord, 0, len(s.managerOf))
	for managed, manager := range s.managerOf {
		out = append(out, managementPairRecord{ManagerLFDI: manager, ManagedLFDI: managed})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ManagedLFDI < out[j].ManagedLFDI })
	return out
}

// persistRecords writes records to disk as the durable snapshot, sorted by
// managed LFDI, or is a no-op for an in-memory (unpersisted) store. The
// caller must hold persistMu for the duration of its whole operation (not
// s.mu: the write happens outside it, GRIDAPPSD/ieee-2030_5-server-go#677
// fix round item 1) and must not apply the in-memory mutation records
// represents until this returns nil: records is the state about to become
// live, written before it does, so a failed write leaves the store exactly
// where its last successful write left it, and a caller who retries lands on
// the same code path rather than an idempotent no-op for a change that never
// took.
func (s *EndDeviceManagementStore) persistRecords(records []managementPairRecord) error {
	if s.persistPath == "" {
		return nil
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ManagedLFDI < records[j].ManagedLFDI })
	if err := writeSnapshotEnvelope(s.persistPath, records); err != nil {
		return fmt.Errorf("enddevice management persistence: %w", err)
	}
	return nil
}
