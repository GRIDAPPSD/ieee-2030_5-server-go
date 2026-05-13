package memory

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

// IEEE-097: durable AdminFSAStore persistence.
//
// Records carry the FSA itself plus the denormalized program-attachments
// and device-assignments (IEEE-096 keeps those next to the FSA — one
// source of truth). Persisting them together means a restart restores the
// entire admin-authored topology in a single pass, with no risk of
// orphaning attachments when the FSA load order differs from the link
// load order.

// adminFSARecord is the on-disk shape per FSA.
type adminFSARecord struct {
	ID       string                       `json:"id"`
	FSA      sep2.FunctionSetAssignments  `json:"fsa"`
	Programs []string                     `json:"programs,omitempty"`
	Devices  []string                     `json:"devices,omitempty"`
}

// NewAdminFSAStoreWithPersistence builds an AdminFSAStore wired to a JSON
// snapshot file. Empty path = pure in-memory (the pre-IEEE-097 behavior).
func NewAdminFSAStoreWithPersistence(path string) (*AdminFSAStore, error) {
	store := NewAdminFSAStore()
	if path == "" {
		return store, nil
	}
	if err := store.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("admin FSA persistence: load %q: %w", path, err)
	}
	store.persistPath = path
	return store, nil
}

func (s *AdminFSAStore) loadFromFile(path string) error {
	env, err := readSnapshotEnvelope(path)
	if err != nil {
		return err
	}
	if env == nil || len(env.Records) == 0 {
		return nil
	}
	var records []adminFSARecord
	if err := json.Unmarshal(env.Records, &records); err != nil {
		return fmt.Errorf("decode records: %w", err)
	}

	// Rehydrate directly into the maps so we keep the sorted-keys
	// invariant in one pass. We are pre-publication so no lock contention.
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range records {
		s.fsas[r.ID] = r.FSA.Copy()
		s.keys = append(s.keys, r.ID)
		if len(r.Programs) > 0 {
			hrefs := append([]string(nil), r.Programs...)
			slices.Sort(hrefs)
			s.programLinks[r.ID] = hrefs
		}
		if len(r.Devices) > 0 {
			devs := append([]string(nil), r.Devices...)
			slices.Sort(devs)
			s.deviceLinks[r.ID] = devs
		}
	}
	slices.Sort(s.keys)
	return nil
}

// snapshotRecords captures the current FSA records + links under the
// store's read lock. Returned slice is independent of internal state.
func (s *AdminFSAStore) snapshotRecords() []adminFSARecord {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]adminFSARecord, 0, len(s.keys))
	for _, k := range s.keys {
		rec := adminFSARecord{
			ID:  k,
			FSA: s.fsas[k].Copy(),
		}
		if hrefs := s.programLinks[k]; len(hrefs) > 0 {
			rec.Programs = append([]string(nil), hrefs...)
		}
		if devs := s.deviceLinks[k]; len(devs) > 0 {
			rec.Devices = append([]string(nil), devs...)
		}
		out = append(out, rec)
	}
	return out
}

// persist flushes the snapshot to disk if persistence is configured.
// No-op for in-memory stores.
func (s *AdminFSAStore) persist() error {
	if s.persistPath == "" {
		return nil
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	records := s.snapshotRecords()
	if err := writeSnapshotEnvelope(s.persistPath, records); err != nil {
		return fmt.Errorf("admin FSA persistence: %w", err)
	}
	return nil
}
