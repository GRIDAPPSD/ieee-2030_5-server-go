package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
// caller was told failed: reads take writeMu's read side, so they wait out a
// writer's mutate, persist and rollback and see only decided state.
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

	writeMu     sync.RWMutex
	persistPath string

	// idOf, when set by WithRecordID, names the id a record carries so a
	// load can refuse a record stored under a different key.
	idOf func(T) string

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

// PersistentScopedOption configures NewPersistentScopedStore.
type PersistentScopedOption[T store.Copier[T]] func(*PersistentScopedStore[T])

// WithRecordID tells the store which id a record carries, so loading refuses
// a snapshot record whose value disagrees with the key it is stored under.
func WithRecordID[T store.Copier[T]](idOf func(T) string) PersistentScopedOption[T] {
	return func(s *PersistentScopedStore[T]) { s.idOf = idOf }
}

// NewPersistentScopedStore builds a store wired to a JSON snapshot at path and
// loads it. An empty path is pure in-memory. label names the collection in
// error messages.
func NewPersistentScopedStore[T store.Copier[T]](path, label string, opts ...PersistentScopedOption[T]) (*PersistentScopedStore[T], error) {
	s := &PersistentScopedStore[T]{
		label: label,
		inner: NewScopedStore[T](),
		keys:  make(map[string][]string),
	}
	for _, o := range opts {
		o(s)
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
		if r.ParentID == "" || r.ID == "" {
			return fmt.Errorf("record with empty parent %q or id %q", r.ParentID, r.ID)
		}
		if s.idOf != nil {
			if got := s.idOf(r.Value); got != r.ID {
				return fmt.Errorf("record %s/%s carries id %q", r.ParentID, r.ID, got)
			}
		}
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
				// %v, not %w: a drifted index must never let a failed write match
				// store.ErrNotFound, which callers read as "already gone".
				return nil, fmt.Errorf("read %s/%s for snapshot: %v", parent, id, err)
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
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return s.inner.Get(ctx, parentID, id)
}

// List returns one page of the records under parentID.
func (s *PersistentScopedStore[T]) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error) {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return s.inner.List(ctx, parentID, opts)
}

// Count returns the number of records under parentID.
func (s *PersistentScopedStore[T]) Count(ctx context.Context, parentID string) (uint32, error) {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return s.inner.Count(ctx, parentID)
}

// HasParent reports whether the collection knows parentID.
func (s *PersistentScopedStore[T]) HasParent(ctx context.Context, parentID string) (bool, error) {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
	return s.inner.HasParent(ctx, parentID)
}

// Parents returns the parent ids the collection knows, ascending.
func (s *PersistentScopedStore[T]) Parents(ctx context.Context) ([]string, error) {
	s.writeMu.RLock()
	defer s.writeMu.RUnlock()
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
		var rerr error
		if hadParent {
			rerr = s.inner.Delete(ctx, parentID, id)
		} else {
			// Detaching the bucket also undoes the parent's existence, which
			// a plain Delete would leave behind as an empty parent.
			_, rerr = s.inner.DeleteParent(ctx, parentID)
		}
		if rerr != nil && !errors.Is(rerr, store.ErrNotFound) {
			// The record is still served, so its key stays indexed: dropping
			// it would leave it out of every later snapshot.
			return errors.Join(err, fmt.Errorf("%s persistence: rollback create %s/%s: %w", s.label, parentID, id, rerr))
		}
		s.dropKey(parentID, id)
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

// DeleteParent removes a parent's whole collection, reports how many records
// went with it, and flushes a snapshot. On a persist failure the records, the
// key index and the parent's existence are restored and the error returns
// with a zero count.
//
// It exists for cascade deletes (the EndDevice delete). Forwarding to the
// inner store directly would skip writeMu, the key index and the snapshot, so
// the cascaded records would be gone in memory and back after a restart.
func (s *PersistentScopedStore[T]) DeleteParent(ctx context.Context, parentID string) (uint32, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	n, _, _, err := s.deleteParentLocked(ctx, parentID)
	return n, err
}

// TakeParent is DeleteParent for a cascade that must be able to put the
// records back when a later step fails: it removes the parent's collection,
// flushes a snapshot, and returns an undo that recreates every removed record
// under the key it was stored under. A failed removal is rolled back before
// it returns and yields no undo.
//
// An undo that cannot restore a record logs that record's parent and id, since
// the same failing snapshot that made the cascade fail is usually what fails
// the restore, and the log is then the only trace of what was lost.
func (s *PersistentScopedStore[T]) TakeParent(ctx context.Context, parentID string) (func(context.Context) error, uint32, error) {
	s.writeMu.Lock()
	n, ids, before, err := s.deleteParentLocked(ctx, parentID)
	s.writeMu.Unlock()
	if err != nil {
		return nil, 0, err
	}
	return func(ctx context.Context) error {
		var errs []error
		for i, id := range ids {
			if err := s.Create(ctx, parentID, id, before[i]); err != nil {
				log.Printf("memory: %s: could not restore %s/%s after a failed cascade: %v", s.label, parentID, id, err)
				errs = append(errs, fmt.Errorf("record %s/%s: %w", parentID, id, err))
			}
		}
		return errors.Join(errs...)
	}, n, nil
}

// deleteParentLocked is DeleteParent with writeMu already held, and with the
// removed ids and records returned for TakeParent.
func (s *PersistentScopedStore[T]) deleteParentLocked(ctx context.Context, parentID string) (n uint32, ids []string, before []T, err error) {
	hadParent, err := s.inner.HasParent(ctx, parentID)
	if err != nil {
		return 0, nil, nil, err
	}
	if !hadParent {
		return 0, nil, nil, nil
	}
	s.keysMu.Lock()
	ids = slices.Clone(s.keys[parentID])
	s.keysMu.Unlock()
	before = make([]T, 0, len(ids))
	for _, id := range ids {
		v, err := s.inner.Get(ctx, parentID, id)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("%s persistence: read %s/%s before delete: %v", s.label, parentID, id, err)
		}
		before = append(before, v)
	}

	n, err = s.inner.DeleteParent(ctx, parentID)
	if err != nil {
		return 0, nil, nil, err
	}
	s.keysMu.Lock()
	delete(s.keys, parentID)
	s.keysMu.Unlock()
	if s.afterMutateBeforePersist != nil {
		s.afterMutateBeforePersist()
	}
	if err := s.persistLocked(ctx); err != nil {
		// ForParent re-establishes the parent even when it held no records.
		s.inner.ForParent(parentID)
		for i, id := range ids {
			if rerr := s.inner.Create(ctx, parentID, id, before[i]); rerr != nil {
				// The records restored so far are served, so their keys
				// must stay indexed or later snapshots would drop them.
				if i > 0 {
					s.keysMu.Lock()
					s.keys[parentID] = ids[:i]
					s.keysMu.Unlock()
				}
				return 0, nil, nil, errors.Join(err, fmt.Errorf("%s persistence: rollback delete parent %s/%s: %w", s.label, parentID, id, rerr))
			}
		}
		s.keysMu.Lock()
		if len(ids) > 0 {
			s.keys[parentID] = ids
		}
		s.keysMu.Unlock()
		return 0, nil, nil, err
	}
	return n, ids, before, nil
}
