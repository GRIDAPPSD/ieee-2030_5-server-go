package memory

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// Durable AdminFSAStore persistence (GRIDAPPSD/ieee-2030_5-server-go#165).
//
// Records carry the FSA itself plus the denormalized program-attachments
// and device-assignments (kept next to the FSA per
// GRIDAPPSD/ieee-2030_5-server-go#163: one source of truth).
// Persisting them together means a restart restores the
// entire admin-authored topology in a single pass, with no risk of
// orphaning attachments when the FSA load order differs from the link
// load order.

// adminFSARecord is the on-disk shape per FSA.
type adminFSARecord struct {
	ID       string                      `json:"id"`
	FSA      sep2.FunctionSetAssignments `json:"fsa"`
	Programs []string                    `json:"programs,omitempty"`
	Devices  []string                    `json:"devices,omitempty"`
	// Templates is absent from snapshots written before templates existed,
	// which therefore load with none.
	Templates []ProgramTemplate `json:"templates,omitempty"`
}

// NewAdminFSAStoreWithPersistence builds an AdminFSAStore wired to a JSON
// snapshot file. Empty path = pure in-memory (the original behavior).
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
		var tmpls []ProgramTemplate
		for _, t := range r.Templates {
			// The write path's own check, so a snapshot cannot hold a
			// template set the store would have refused.
			if err := checkTemplateAdd(tmpls, t); err != nil {
				return fmt.Errorf("FSA %q template %q: %w", r.ID, t.ID, err)
			}
			tmpls = append(tmpls, t.Copy())
		}
		if len(tmpls) > 0 {
			slices.SortFunc(tmpls, func(a, b ProgramTemplate) int { return strings.Compare(a.ID, b.ID) })
			s.templates[r.ID] = tmpls
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
		for _, t := range s.templates[k] {
			rec.Templates = append(rec.Templates, t.Copy())
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
