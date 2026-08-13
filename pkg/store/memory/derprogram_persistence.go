package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// DERProgramStore persistence (GRIDAPPSD/ieee-2030_5-server-go#165).
//
// DERPrograms are addressed as (parent, id) pairs: a parent is a device key and
// each parent holds its own programs. The persistence wrapper flattens those
// tuples into a single snapshot; rehydration walks the snapshot and re-Creates
// each tuple, which rebuilds the same scoped layout.
//
// # It holds the collection, it does not embed it
//
// This wrapper used to embed *ScopedStore[sep2.DERProgram], promoting Get,
// List, Count, HasParent and Parents while shadowing Create and Delete. Two
// things followed from that and neither was wanted. Its conformance to
// [store.ScopedStore] was an accident of what the embedded type provided, so a
// method added to the contract would have been answered by the inner store with
// no flush and no compile error. And the embedding leaked ForParent, which is
// deliberately absent from the contract, so a caller reaching for a per-parent
// handle found one on the persistence wrapper of all places. Callers that want
// one parent's collection now take [store.Under].
//
// # Why the wrapper keeps its own key index
//
// The reasoning is [RegistrationStore]'s and is not repeated: the contract has
// no key enumeration, and deriving a key from a field of the resource would tie
// durable identity to a value a writer controls. Parents() would answer half
// the question, the parent ids, and nothing answers the other half, so the
// wrapper records the (parent, id) pairs it writes and reads each back at
// snapshot time.
type DERProgramStore struct {
	// inner is the collection this wrapper owns, declared as the contract.
	inner store.ScopedStore[sep2.DERProgram]

	// keysMu guards keys: parent id to the ascending ids written under it.
	keysMu sync.Mutex
	keys   map[string][]string

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
		inner: NewScopedStore[sep2.DERProgram](),
		keys:  make(map[string][]string),
	}
}

// NewDERProgramStoreWithPersistence builds a DERProgramStore wired to a
// JSON snapshot file. Empty path = pure in-memory.
func NewDERProgramStoreWithPersistence(path string) (*DERProgramStore, error) {
	s := NewDERProgramStore()
	if path == "" {
		return s, nil
	}
	if err := s.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("derprogram persistence: load %q: %w", path, err)
	}
	s.persistPath = path
	return s, nil
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
		// Through this wrapper's own Create, so the key index is populated by
		// the path that maintains it during service. persistPath is still
		// empty here, so reading the snapshot cannot write one back.
		if err := s.Create(ctx, r.ParentID, r.ID, r.Program); err != nil {
			return fmt.Errorf("rehydrate %s/%s: %w", r.ParentID, r.ID, err)
		}
	}
	return nil
}

// addKey records (parentID, id) as present, keeping each parent's ids sorted.
func (s *DERProgramStore) addKey(parentID, id string) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	ids := s.keys[parentID]
	if idx, found := slices.BinarySearch(ids, id); !found {
		s.keys[parentID] = slices.Insert(ids, idx, id)
	}
}

// dropKey records (parentID, id) as absent. A parent left holding nothing is
// removed from the index: it contributes no records, and keeping it would make
// the index grow without bound under a create-delete cycle.
func (s *DERProgramStore) dropKey(parentID, id string) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	ids := s.keys[parentID]
	if idx, found := slices.BinarySearch(ids, id); found {
		ids = slices.Delete(ids, idx, idx+1)
		if len(ids) == 0 {
			delete(s.keys, parentID)
			return
		}
		s.keys[parentID] = ids
	}
}

// snapshotKeys returns an independent copy of the key index, with parents in
// ascending order so a snapshot's record order is deterministic.
func (s *DERProgramStore) snapshotKeys() []string {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	return slices.Sorted(maps.Keys(s.keys))
}

// idsUnder returns an independent copy of the ids recorded under parentID.
func (s *DERProgramStore) idsUnder(parentID string) []string {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	return slices.Clone(s.keys[parentID])
}

// snapshotRecords reads back every recorded pair and emits the flat tuple list.
//
// The skip-on-missing and abort-on-anything-else rule is [RegistrationStore]'s
// and holds for the same reason: a record removed concurrently is described by
// the snapshot that removal is about to write, while a record that could not be
// READ must not be quietly dropped from the file, because on disk a dropped
// record and a deleted one are the same thing.
func (s *DERProgramStore) snapshotRecords(ctx context.Context) ([]derProgramRecord, error) {
	var out []derProgramRecord
	for _, parent := range s.snapshotKeys() {
		for _, id := range s.idsUnder(parent) {
			program, err := s.inner.Get(ctx, parent, id)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				return nil, fmt.Errorf("read %s/%s for snapshot: %w", parent, id, err)
			}
			out = append(out, derProgramRecord{
				ParentID: parent,
				ID:       id,
				Program:  program,
			})
		}
	}
	return out, nil
}

func (s *DERProgramStore) persist(ctx context.Context) error {
	if s.persistPath == "" {
		return nil
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	records, err := s.snapshotRecords(ctx)
	if err != nil {
		return fmt.Errorf("derprogram persistence: %w", err)
	}
	if err := writeSnapshotEnvelope(s.persistPath, records); err != nil {
		return fmt.Errorf("derprogram persistence: %w", err)
	}
	return nil
}

// Get returns the program stored under (parentID, id), or store.ErrNotFound.
func (s *DERProgramStore) Get(ctx context.Context, parentID, id string) (sep2.DERProgram, error) {
	return s.inner.Get(ctx, parentID, id)
}

// List returns one page of the programs under parentID.
func (s *DERProgramStore) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERProgram], error) {
	return s.inner.List(ctx, parentID, opts)
}

// Count returns the number of programs under parentID.
func (s *DERProgramStore) Count(ctx context.Context, parentID string) (uint32, error) {
	return s.inner.Count(ctx, parentID)
}

// HasParent reports whether the collection knows parentID.
func (s *DERProgramStore) HasParent(ctx context.Context, parentID string) (bool, error) {
	return s.inner.HasParent(ctx, parentID)
}

// Parents returns the parent ids the collection knows, ascending.
func (s *DERProgramStore) Parents(ctx context.Context) ([]string, error) {
	return s.inner.Parents(ctx)
}

// Create stores a program and flushes a snapshot.
func (s *DERProgramStore) Create(ctx context.Context, parentID, id string, program sep2.DERProgram) error {
	if err := s.inner.Create(ctx, parentID, id, program); err != nil {
		return err
	}
	s.addKey(parentID, id)
	return s.persist(ctx)
}

// Update replaces a program.
//
// It does NOT flush a snapshot, which is what this wrapper did before it was
// re-expressed against the contract: Update was promoted off the embedded store
// and never reached the persistence path, so an updated program reverted to its
// pre-update form on restart. Preserved verbatim here rather than corrected,
// because this fix is a type-level conversion and changing what is written
// to disk is a behaviour change. Reported separately as a finding.
func (s *DERProgramStore) Update(ctx context.Context, parentID, id string, program sep2.DERProgram) error {
	return s.inner.Update(ctx, parentID, id, program)
}

// Delete removes a program and flushes a snapshot.
func (s *DERProgramStore) Delete(ctx context.Context, parentID, id string) error {
	if err := s.inner.Delete(ctx, parentID, id); err != nil {
		return err
	}
	s.dropKey(parentID, id)
	return s.persist(ctx)
}
