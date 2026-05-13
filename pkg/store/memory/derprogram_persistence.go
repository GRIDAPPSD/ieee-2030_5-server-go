package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
)

// IEEE-097: DERProgramStore persistence.
//
// DERPrograms historically live in *ScopedStore[sep2.DERProgram] —
// each parent (device ID) has its own keyed sub-store. The persistence
// wrapper flattens (parent, id, program) tuples into a single snapshot;
// rehydration walks the snapshot and re-Creates each tuple, which
// rebuilds the same scoped layout.
//
// DERProgramStore embeds *ScopedStore[sep2.DERProgram] so the existing
// call sites (router.go: stores.DERPrograms.ForParent(...), etc.) keep
// compiling against the wrapper without source changes. Create/Delete
// shadow the embedded methods to add the disk flush.

// DERProgramStore wraps the scoped DERProgram store with disk persistence.
type DERProgramStore struct {
	*ScopedStore[sep2.DERProgram]

	persistMu   sync.Mutex
	persistPath string
}

// derProgramRecord is the on-disk shape: parent ID, program ID, program.
// Flat tuples re-create the scoped layout on rehydration via Create().
type derProgramRecord struct {
	ParentID string          `json:"parent"`
	ID       string          `json:"id"`
	Program  sep2.DERProgram `json:"program"`
}

// NewDERProgramStore creates an in-memory DERProgramStore.
func NewDERProgramStore() *DERProgramStore {
	return &DERProgramStore{
		ScopedStore: NewScopedStore[sep2.DERProgram](),
	}
}

// NewDERProgramStoreWithPersistence builds a DERProgramStore wired to a
// JSON snapshot file. Empty path = pure in-memory.
func NewDERProgramStoreWithPersistence(path string) (*DERProgramStore, error) {
	store := NewDERProgramStore()
	if path == "" {
		return store, nil
	}
	if err := store.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("derprogram persistence: load %q: %w", path, err)
	}
	store.persistPath = path
	return store, nil
}

func (s *DERProgramStore) loadFromFile(path string) error {
	env, err := readSnapshotEnvelope(path)
	if err != nil {
		return err
	}
	if env == nil || len(env.Records) == 0 {
		return nil
	}
	var records []derProgramRecord
	if err := json.Unmarshal(env.Records, &records); err != nil {
		return fmt.Errorf("decode records: %w", err)
	}
	ctx := context.Background()
	for _, r := range records {
		if err := s.ScopedStore.Create(ctx, r.ParentID, r.ID, r.Program); err != nil {
			return fmt.Errorf("rehydrate %s/%s: %w", r.ParentID, r.ID, err)
		}
	}
	return nil
}

// snapshotRecords walks the scoped store and emits the flat tuple list.
// Holds the outer mu read lock to keep the parent-set stable while
// iterating; each inner Store's read lock is taken via its public API.
func (s *DERProgramStore) snapshotRecords() []derProgramRecord {
	s.ScopedStore.mu.RLock()
	parents := make([]string, 0, len(s.ScopedStore.stores))
	innerStores := make(map[string]*Store[sep2.DERProgram], len(s.ScopedStore.stores))
	for k, v := range s.ScopedStore.stores {
		parents = append(parents, k)
		innerStores[k] = v
	}
	s.ScopedStore.mu.RUnlock()

	var out []derProgramRecord
	for _, parent := range parents {
		inner := innerStores[parent]
		inner.mu.RLock()
		for _, id := range inner.keys {
			out = append(out, derProgramRecord{
				ParentID: parent,
				ID:       id,
				Program:  inner.data[id].Copy(),
			})
		}
		inner.mu.RUnlock()
	}
	return out
}

func (s *DERProgramStore) persist() error {
	if s.persistPath == "" {
		return nil
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	records := s.snapshotRecords()
	if err := writeSnapshotEnvelope(s.persistPath, records); err != nil {
		return fmt.Errorf("derprogram persistence: %w", err)
	}
	return nil
}

// Create shadows ScopedStore.Create to add the persistence flush.
func (s *DERProgramStore) Create(ctx context.Context, parentID, id string, program sep2.DERProgram) error {
	if err := s.ScopedStore.Create(ctx, parentID, id, program); err != nil {
		return err
	}
	return s.persist()
}

// Delete shadows ScopedStore.Delete to add the persistence flush.
func (s *DERProgramStore) Delete(ctx context.Context, parentID, id string) error {
	if err := s.ScopedStore.Delete(ctx, parentID, id); err != nil {
		return err
	}
	return s.persist()
}
