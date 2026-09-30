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

// DERControlStore persistence (GRIDAPPSD/ieee-2030_5-server-go#565).
//
// Shaped after [DERProgramStore]: DERControls are addressed as (parent, id)
// pairs, so a snapshot is a flat list of tuples that Create replays on
// rehydration. It holds the collection rather than embedding it, and it does
// not expose ForParent; callers that want one parent's collection take
// [store.Under], for the reasons [DERProgramStore]'s doc comment gives.
//
// # Rollback on a failed snapshot write
//
// Unlike [DERProgramStore] and [RegistrationStore], a write here that stores
// successfully in memory but fails to reach disk is undone before the error
// returns: the in-memory collection, the key index and the mRID index are all
// reverted to what they held before the call. Acceptance criterion 4 requires
// this because internal/dercontrol.Issuer treats a *DERControl* Create
// failure as possibly-partial and always issues a Delete to compensate; if
// this store instead kept the record in memory on a disk failure, that
// compensating Delete would (correctly) remove it, but any reader racing
// between the failed Create and the compensating Delete would briefly see a
// control that was never durably stored. Rolling back inside Create closes
// that window instead of relying on a caller-side compensation.
type DERControlStore struct {
	// inner is the collection this wrapper owns, declared as the contract.
	inner store.ScopedStore[sep2.DERControl]

	// keysMu guards keys: parent id to the ascending ids written under it.
	keysMu sync.Mutex
	keys   map[string][]string

	// mridMu guards mrid: the mRID-to-scope index used for lookup by mRID
	// (acceptance criterion 5). Rebuilt from the reloaded records on every
	// NewDERControlStoreWithPersistence call, since loadFromFile replays
	// through this wrapper's own Create.
	mridMu sync.Mutex
	mrid   map[string]derControlKey

	// writeMu serializes every Create, Update and Delete end to end: the
	// in-memory mutation, the key and mRID index updates, the snapshot
	// write, and any rollback if persist fails. An earlier version locked
	// only around the snapshot write, which left a window between one
	// writer's mutation and its own persist/rollback decision where a
	// SECOND writer's persist call (for a different id) could run,
	// snapshot the whole store including the first writer's not-yet-
	// committed record, and durably write it to disk. If the first
	// writer's own persist then failed and rolled back, the caller was
	// told Create failed while the record was, in fact, on disk: after a
	// restart the "failed" control is served, and it has no lifecycle
	// record, so Issuer.Cancel refuses it with RefusalControlNotFound
	// even though the aggregator can see it (GRIDAPPSD/ieee-2030_5-server-go#565
	// fix round 1, item 1). Holding one lock across the whole sequence
	// means no writer's persist can ever observe another writer's
	// in-flight, not-yet-decided mutation.
	writeMu sync.Mutex

	persistPath string

	// afterMutateBeforePersist, when non-nil, runs once inside a writer's
	// critical section, after the in-memory mutation (and index updates)
	// and before persistLocked is attempted. Nil in production; set only
	// by dercontrol_persistence_race_test.go to force a deterministic
	// interleaving between two writers.
	afterMutateBeforePersist func()
}

// derControlKey addresses one control by (parent, id).
type derControlKey struct {
	ParentID string
	ID       string
}

// derControlRecord is the on-disk shape: parent ID, control ID, control.
type derControlRecord struct {
	ParentID string          `json:"parent"`
	ID       string          `json:"id"`
	Control  sep2.DERControl `json:"control"`
}

// NewDERControlStore creates an in-memory DERControlStore.
func NewDERControlStore() *DERControlStore {
	return &DERControlStore{
		inner: NewScopedStore[sep2.DERControl](),
		keys:  make(map[string][]string),
		mrid:  make(map[string]derControlKey),
	}
}

// NewDERControlStoreWithPersistence builds a DERControlStore wired to a JSON
// snapshot file. Empty path = pure in-memory.
func NewDERControlStoreWithPersistence(path string) (*DERControlStore, error) {
	s := NewDERControlStore()
	if path == "" {
		return s, nil
	}
	if err := s.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("dercontrol persistence: load %q: %w", path, err)
	}
	s.persistPath = path
	return s, nil
}

// Persists reports whether this store is wired to a snapshot path
// (acceptance criterion 2): the admin plane uses this to report whether an
// issued control will survive a restart.
func (s *DERControlStore) Persists() bool {
	return s.persistPath != ""
}

func (s *DERControlStore) loadFromFile(path string) error {
	env, err := readSnapshotEnvelope(path)
	if err != nil {
		return err
	}
	if env == nil || len(env.Records) == 0 {
		return nil
	}
	var records []derControlRecord
	if err := json.Unmarshal(env.Records, &records); err != nil {
		return fmt.Errorf("decode records: %w", err)
	}
	ctx := context.Background()
	for _, r := range records {
		// Through this wrapper's own Create, so the key and mRID indexes are
		// populated by the path that maintains them during service.
		// persistPath is still empty here, so reading the snapshot cannot
		// write one back.
		if err := s.Create(ctx, r.ParentID, r.ID, r.Control); err != nil {
			return fmt.Errorf("rehydrate %s/%s: %w", r.ParentID, r.ID, err)
		}
	}
	return nil
}

func (s *DERControlStore) addKey(parentID, id string) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	ids := s.keys[parentID]
	if idx, found := slices.BinarySearch(ids, id); !found {
		s.keys[parentID] = slices.Insert(ids, idx, id)
	}
}

func (s *DERControlStore) dropKey(parentID, id string) {
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

func (s *DERControlStore) snapshotKeys() []string {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	return slices.Sorted(maps.Keys(s.keys))
}

func (s *DERControlStore) idsUnder(parentID string) []string {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	return slices.Clone(s.keys[parentID])
}

// addMRID records mRID as addressed at key. A blank mRID is never indexed:
// v1 controls always mint one (internal/dercontrol.newMRID), but a control
// this package did not create (a boot fixture or the CSIP loader) may have
// none, and an empty key would collide across every such control.
func (s *DERControlStore) addMRID(mrid string, key derControlKey) {
	if mrid == "" {
		return
	}
	s.mridMu.Lock()
	defer s.mridMu.Unlock()
	s.mrid[mrid] = key
}

func (s *DERControlStore) dropMRID(mrid string) {
	if mrid == "" {
		return
	}
	s.mridMu.Lock()
	defer s.mridMu.Unlock()
	delete(s.mrid, mrid)
}

// ByMRID returns the control stored under the given mRID, or ErrNotFound.
// The index is rebuilt from the reloaded stores at construction time
// (acceptance criterion 5), so this answers correctly for a control found
// only after a restart, not only one created in this process.
func (s *DERControlStore) ByMRID(ctx context.Context, mrid string) (parentID, id string, control sep2.DERControl, err error) {
	s.mridMu.Lock()
	key, ok := s.mrid[mrid]
	s.mridMu.Unlock()
	if !ok {
		return "", "", sep2.DERControl{}, store.ErrNotFound
	}
	control, err = s.inner.Get(ctx, key.ParentID, key.ID)
	if err != nil {
		return "", "", sep2.DERControl{}, err
	}
	return key.ParentID, key.ID, control, nil
}

func (s *DERControlStore) snapshotRecords(ctx context.Context) ([]derControlRecord, error) {
	var out []derControlRecord
	for _, parent := range s.snapshotKeys() {
		for _, id := range s.idsUnder(parent) {
			ctrl, err := s.inner.Get(ctx, parent, id)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					// A concurrent Delete removed it; the snapshot that
					// Delete is about to write already describes this state.
					continue
				}
				return nil, fmt.Errorf("read %s/%s for snapshot: %w", parent, id, err)
			}
			out = append(out, derControlRecord{ParentID: parent, ID: id, Control: ctrl})
		}
	}
	return out, nil
}

// persistLocked snapshots the current state and writes it to disk. The
// caller must already hold writeMu: see the type doc comment for why the
// whole mutate-then-persist-then-rollback sequence, not just this call,
// has to run under one lock.
func (s *DERControlStore) persistLocked(ctx context.Context) error {
	if s.persistPath == "" {
		return nil
	}

	records, err := s.snapshotRecords(ctx)
	if err != nil {
		return fmt.Errorf("dercontrol persistence: %w", err)
	}
	if err := writeSnapshotEnvelope(s.persistPath, records); err != nil {
		return fmt.Errorf("dercontrol persistence: %w", err)
	}
	return nil
}

// Get returns the control stored under (parentID, id), or store.ErrNotFound.
func (s *DERControlStore) Get(ctx context.Context, parentID, id string) (sep2.DERControl, error) {
	return s.inner.Get(ctx, parentID, id)
}

// List returns one page of the controls under parentID.
func (s *DERControlStore) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERControl], error) {
	return s.inner.List(ctx, parentID, opts)
}

// Count returns the number of controls under parentID.
func (s *DERControlStore) Count(ctx context.Context, parentID string) (uint32, error) {
	return s.inner.Count(ctx, parentID)
}

// HasParent reports whether the collection knows parentID.
func (s *DERControlStore) HasParent(ctx context.Context, parentID string) (bool, error) {
	return s.inner.HasParent(ctx, parentID)
}

// Parents returns the parent ids the collection knows, ascending.
func (s *DERControlStore) Parents(ctx context.Context) ([]string, error) {
	return s.inner.Parents(ctx)
}

// Create stores a control and flushes a snapshot. On a persist failure the
// in-memory write, the key index and the mRID index are all rolled back
// before the error returns (see the type doc comment): the caller never
// observes a Get or ByMRID succeeding for a write this call reported as
// failed.
func (s *DERControlStore) Create(ctx context.Context, parentID, id string, control sep2.DERControl) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.inner.Create(ctx, parentID, id, control); err != nil {
		return err
	}
	key := derControlKey{ParentID: parentID, ID: id}
	s.addKey(parentID, id)
	s.addMRID(control.MRID, key)
	if s.afterMutateBeforePersist != nil {
		s.afterMutateBeforePersist()
	}
	if err := s.persistLocked(ctx); err != nil {
		if derr := s.inner.Delete(ctx, parentID, id); derr != nil && !errors.Is(derr, store.ErrNotFound) {
			return errors.Join(err, fmt.Errorf("dercontrol persistence: rollback create %s/%s: %w", parentID, id, derr))
		}
		s.dropKey(parentID, id)
		s.dropMRID(control.MRID)
		return err
	}
	return nil
}

// Update replaces a control and flushes a snapshot, rolling back to the
// prior value on a persist failure (see the type doc comment).
func (s *DERControlStore) Update(ctx context.Context, parentID, id string, control sep2.DERControl) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	before, err := s.inner.Get(ctx, parentID, id)
	if err != nil {
		return err
	}
	if err := s.inner.Update(ctx, parentID, id, control); err != nil {
		return err
	}
	key := derControlKey{ParentID: parentID, ID: id}
	if before.MRID != control.MRID {
		s.dropMRID(before.MRID)
		s.addMRID(control.MRID, key)
	}
	if s.afterMutateBeforePersist != nil {
		s.afterMutateBeforePersist()
	}
	if err := s.persistLocked(ctx); err != nil {
		if derr := s.inner.Update(ctx, parentID, id, before); derr != nil {
			return errors.Join(err, fmt.Errorf("dercontrol persistence: rollback update %s/%s: %w", parentID, id, derr))
		}
		if before.MRID != control.MRID {
			s.dropMRID(control.MRID)
			s.addMRID(before.MRID, key)
		}
		return err
	}
	return nil
}

// Delete removes a control and flushes a snapshot, restoring the deleted
// record on a persist failure (see the type doc comment).
func (s *DERControlStore) Delete(ctx context.Context, parentID, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	before, err := s.inner.Get(ctx, parentID, id)
	if err != nil {
		return err
	}
	if err := s.inner.Delete(ctx, parentID, id); err != nil {
		return err
	}
	s.dropKey(parentID, id)
	s.dropMRID(before.MRID)
	if s.afterMutateBeforePersist != nil {
		s.afterMutateBeforePersist()
	}
	if err := s.persistLocked(ctx); err != nil {
		if derr := s.inner.Create(ctx, parentID, id, before); derr != nil {
			return errors.Join(err, fmt.Errorf("dercontrol persistence: rollback delete %s/%s: %w", parentID, id, derr))
		}
		s.addKey(parentID, id)
		s.addMRID(before.MRID, derControlKey{ParentID: parentID, ID: id})
		return err
	}
	return nil
}
