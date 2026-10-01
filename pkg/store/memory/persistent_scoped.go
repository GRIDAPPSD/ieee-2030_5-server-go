package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// PersistentScopedStore is a parent-scoped collection of any record type that
// reloads from a JSON snapshot at start (GRIDAPPSD/ieee-2030_5-server-go#760).
// It is [DERControlStore] without the mRID index, with the same semantics.
//
// A write that stores in memory but fails to reach disk is undone before the
// error returns: the collection, the key index and the parent's existence all
// return to what they held before the call. A caller that treats a failed
// write as "not stored" is then never wrong, and no reader sees a record the
// caller was told failed.
//
// writeMu is held across mutate, persist and rollback, not just the file
// write. Locking only the file write lets a second writer snapshot the whole
// collection, including the first writer's undecided record, and persist it;
// if the first writer then rolls back, the record is on disk though its Create
// reported failure, and a restart serves it.
//
// A parent emptied by Delete is not written to the snapshot, so it does not
// survive a restart; Count is the question to ask, per store.ScopedReader.
type PersistentScopedStore[T store.Copier[T]] struct {
	label string
	inner *ScopedStore[T]

	// keysMu guards keys: parent id to the ascending ids written under it.
	// The inner store cannot enumerate keys, so snapshots walk this index.
	keysMu sync.Mutex
	keys   map[string][]string

	writeMu     sync.Mutex
	persistPath string

	// afterMutateBeforePersist, when non-nil, runs inside a writer's critical
	// section between the in-memory mutation and the persist attempt. Nil in
	// production; the race test sets it to force an interleaving.
	afterMutateBeforePersist func()
}

// persistentScopedRecord is the on-disk shape: parent id, record id, record.
type persistentScopedRecord[T any] struct {
	ParentID string `json:"parent"`
	ID       string `json:"id"`
	Value    T      `json:"value"`
}

// NewPersistentScopedStore builds a store wired to a JSON snapshot at path and
// loads it. An empty path is pure in-memory. label names the collection in
// error messages.
func NewPersistentScopedStore[T store.Copier[T]](path, label string) (*PersistentScopedStore[T], error) {
	s := &PersistentScopedStore[T]{
		label: label,
		inner: NewScopedStore[T](),
		keys:  make(map[string][]string),
	}
	if path == "" {
		return s, nil
	}
	if err := s.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("%s persistence: load %q: %w", label, path, err)
	}
	s.persistPath = path
	return s, nil
}

// Persists reports whether the store is wired to a snapshot path.
func (s *PersistentScopedStore[T]) Persists() bool {
	return s.persistPath != ""
}

// RollsBackOnFailure reports true: a failed Create, Update or Delete restores
// the pre-call state, so a caller need not compensate with another write.
func (s *PersistentScopedStore[T]) RollsBackOnFailure() bool {
	return true
}

func (s *PersistentScopedStore[T]) loadFromFile(path string) error {
	env, err := readSnapshotEnvelope(path)
	if err != nil {
		return err
	}
	if env == nil || len(env.Records) == 0 {
		return nil
	}
	var records []persistentScopedRecord[T]
	if err := json.Unmarshal(env.Records, &records); err != nil {
		return fmt.Errorf("decode records: %w", err)
	}
	ctx := context.Background()
	for _, r := range records {
		// Through this store's own Create so the key index is built by the
		// path that maintains it in service. persistPath is still empty, so
		// loading cannot write a snapshot back.
		if err := s.Create(ctx, r.ParentID, r.ID, r.Value); err != nil {
			return fmt.Errorf("rehydrate %s/%s: %w", r.ParentID, r.ID, err)
		}
	}
	return nil
}

func (s *PersistentScopedStore[T]) addKey(parentID, id string) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	ids := s.keys[parentID]
	if idx, found := slices.BinarySearch(ids, id); !found {
		s.keys[parentID] = slices.Insert(ids, idx, id)
	}
}

func (s *PersistentScopedStore[T]) dropKey(parentID, id string) {
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

func (s *PersistentScopedStore[T]) snapshotRecords(ctx context.Context) ([]persistentScopedRecord[T], error) {
	s.keysMu.Lock()
	parents := slices.Sorted(maps.Keys(s.keys))
	idsByParent := make(map[string][]string, len(parents))
	for _, p := range parents {
		idsByParent[p] = slices.Clone(s.keys[p])
	}
	s.keysMu.Unlock()

	var out []persistentScopedRecord[T]
	for _, parent := range parents {
		for _, id := range idsByParent[parent] {
			v, err := s.inner.Get(ctx, parent, id)
			if err != nil {
				return nil, fmt.Errorf("read %s/%s for snapshot: %w", parent, id, err)
			}
			out = append(out, persistentScopedRecord[T]{ParentID: parent, ID: id, Value: v})
		}
	}
	return out, nil
}

// persistLocked writes the current state. The caller holds writeMu.
func (s *PersistentScopedStore[T]) persistLocked(ctx context.Context) error {
	if s.persistPath == "" {
		return nil
	}
	records, err := s.snapshotRecords(ctx)
	if err != nil {
		return fmt.Errorf("%s persistence: %w", s.label, err)
	}
	if err := writeSnapshotEnvelope(s.persistPath, records); err != nil {
		return fmt.Errorf("%s persistence: %w", s.label, err)
	}
	return nil
}

// Get returns the record under (parentID, id), or store.ErrNotFound.
func (s *PersistentScopedStore[T]) Get(ctx context.Context, parentID, id string) (T, error) {
	return s.inner.Get(ctx, parentID, id)
}

// List returns one page of the records under parentID.
func (s *PersistentScopedStore[T]) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	return s.inner.List(ctx, parentID, opts)
}

// Count returns the number of records under parentID.
func (s *PersistentScopedStore[T]) Count(ctx context.Context, parentID string) (uint32, error) {
	return s.inner.Count(ctx, parentID)
}

// HasParent reports whether the collection knows parentID.
func (s *PersistentScopedStore[T]) HasParent(ctx context.Context, parentID string) (bool, error) {
	return s.inner.HasParent(ctx, parentID)
}

// Parents returns the parent ids the collection knows, ascending.
func (s *PersistentScopedStore[T]) Parents(ctx context.Context) ([]string, error) {
	return s.inner.Parents(ctx)
}

// Create stores a record and flushes a snapshot, rolling the record, its key
// and a parent it newly established back out on a persist failure.
func (s *PersistentScopedStore[T]) Create(ctx context.Context, parentID, id string, value T) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	hadParent, err := s.inner.HasParent(ctx, parentID)
	if err != nil {
		return err
	}
	if err := s.inner.Create(ctx, parentID, id, value); err != nil {
		return err
	}
	s.addKey(parentID, id)
	if s.afterMutateBeforePersist != nil {
		s.afterMutateBeforePersist()
	}
	if err := s.persistLocked(ctx); err != nil {
		s.dropKey(parentID, id)
		var rerr error
		if hadParent {
			rerr = s.inner.Delete(ctx, parentID, id)
		} else {
			// Detaching the bucket also undoes the parent's existence, which
			// a plain Delete would leave behind as an empty parent.
			_, rerr = s.inner.DeleteParent(ctx, parentID)
		}
		if rerr != nil && !errors.Is(rerr, store.ErrNotFound) {
			return errors.Join(err, fmt.Errorf("%s persistence: rollback create %s/%s: %w", s.label, parentID, id, rerr))
		}
		return err
	}
	return nil
}

// Update replaces a record and flushes a snapshot, restoring the prior value
// on a persist failure.
func (s *PersistentScopedStore[T]) Update(ctx context.Context, parentID, id string, value T) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	before, err := s.inner.Get(ctx, parentID, id)
	if err != nil {
		return err
	}
	if err := s.inner.Update(ctx, parentID, id, value); err != nil {
		return err
	}
	if s.afterMutateBeforePersist != nil {
		s.afterMutateBeforePersist()
	}
	if err := s.persistLocked(ctx); err != nil {
		if rerr := s.inner.Update(ctx, parentID, id, before); rerr != nil {
			return errors.Join(err, fmt.Errorf("%s persistence: rollback update %s/%s: %w", s.label, parentID, id, rerr))
		}
		return err
	}
	return nil
}

// Delete removes a record and flushes a snapshot, restoring the record on a
// persist failure.
func (s *PersistentScopedStore[T]) Delete(ctx context.Context, parentID, id string) error {
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
	if s.afterMutateBeforePersist != nil {
		s.afterMutateBeforePersist()
	}
	if err := s.persistLocked(ctx); err != nil {
		if rerr := s.inner.Create(ctx, parentID, id, before); rerr != nil {
			return errors.Join(err, fmt.Errorf("%s persistence: rollback delete %s/%s: %w", s.label, parentID, id, rerr))
		}
		s.addKey(parentID, id)
		return err
	}
	return nil
}
