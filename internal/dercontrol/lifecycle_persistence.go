package dercontrol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/atomicfile"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// LifecycleStore persistence (GRIDAPPSD/ieee-2030_5-server-go#565).
//
// A LifecycleRecord is addressed exactly as its companion DERControl is: the
// same (scope, id) pair. The on-disk envelope, versioning and atomic-write
// mechanics mirror pkg/store/memory/persistence.go and
// pkg/store/memory/dercontrol_persistence.go rather than importing them:
// LifecycleRecord is this package's own type, and pkg/store/memory only ever
// knows about sep2 wire types (see DERControlStore's doc comment). This
// package already duplicates the same atomicfile-plus-JSON-envelope shape
// internal/bootfixture/seedrecord.go uses for its own record type, for the
// same reason.
type LifecycleStore struct {
	inner store.ScopedStore[LifecycleRecord]

	keysMu sync.Mutex
	keys   map[string][]string

	persistMu   sync.Mutex
	persistPath string
}

const lifecyclePersistenceVersion = 1

// lifecycleEnvelope is the root JSON document written to disk.
type lifecycleEnvelope struct {
	Version int                   `json:"version"`
	Records []lifecycleDiskRecord `json:"records"`
}

// lifecycleDiskRecord is the on-disk shape: parent (scope key), control id,
// the lifecycle record.
type lifecycleDiskRecord struct {
	ParentID string          `json:"parent"`
	ID       string          `json:"id"`
	Record   LifecycleRecord `json:"record"`
}

// NewLifecycleStore creates an in-memory LifecycleStore.
func NewLifecycleStore() *LifecycleStore {
	return &LifecycleStore{
		inner: memory.NewScopedStore[LifecycleRecord](),
		keys:  make(map[string][]string),
	}
}

// NewLifecycleStoreWithPersistence builds a LifecycleStore wired to a JSON
// snapshot file. Empty path = pure in-memory.
func NewLifecycleStoreWithPersistence(path string) (*LifecycleStore, error) {
	s := NewLifecycleStore()
	if path == "" {
		return s, nil
	}
	if err := s.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("dercontrol lifecycle persistence: load %q: %w", path, err)
	}
	s.persistPath = path
	return s, nil
}

// Persists reports whether this store is wired to a snapshot path
// (acceptance criterion 2).
func (s *LifecycleStore) Persists() bool {
	return s.persistPath != ""
}

func readLifecycleEnvelope(path string) (*lifecycleEnvelope, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	if len(data) == 0 {
		return &lifecycleEnvelope{Version: lifecyclePersistenceVersion}, nil
	}
	var env lifecycleEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("decode %q: %w", path, err)
	}
	if env.Version != lifecyclePersistenceVersion {
		return nil, fmt.Errorf("unsupported snapshot version %d at %q (want %d)",
			env.Version, path, lifecyclePersistenceVersion)
	}
	return &env, nil
}

func writeLifecycleEnvelope(path string, records []lifecycleDiskRecord) error {
	payload, err := json.Marshal(lifecycleEnvelope{Version: lifecyclePersistenceVersion, Records: records})
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	return atomicfile.Write(path, payload)
}

func (s *LifecycleStore) loadFromFile(path string) error {
	env, err := readLifecycleEnvelope(path)
	if err != nil {
		return err
	}
	if env == nil || len(env.Records) == 0 {
		return nil
	}
	ctx := context.Background()
	for _, r := range env.Records {
		// Through this wrapper's own Create, so the key index is populated by
		// the path that maintains it during service.
		if err := s.Create(ctx, r.ParentID, r.ID, r.Record); err != nil {
			return fmt.Errorf("rehydrate %s/%s: %w", r.ParentID, r.ID, err)
		}
	}
	return nil
}

func (s *LifecycleStore) addKey(parentID, id string) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	ids := s.keys[parentID]
	if idx, found := slices.BinarySearch(ids, id); !found {
		s.keys[parentID] = slices.Insert(ids, idx, id)
	}
}

func (s *LifecycleStore) dropKey(parentID, id string) {
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

func (s *LifecycleStore) snapshotKeys() []string {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	return slices.Sorted(maps.Keys(s.keys))
}

func (s *LifecycleStore) idsUnder(parentID string) []string {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	return slices.Clone(s.keys[parentID])
}

func (s *LifecycleStore) snapshotRecords(ctx context.Context) ([]lifecycleDiskRecord, error) {
	var out []lifecycleDiskRecord
	for _, parent := range s.snapshotKeys() {
		for _, id := range s.idsUnder(parent) {
			rec, err := s.inner.Get(ctx, parent, id)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue
				}
				return nil, fmt.Errorf("read %s/%s for snapshot: %w", parent, id, err)
			}
			out = append(out, lifecycleDiskRecord{ParentID: parent, ID: id, Record: rec})
		}
	}
	return out, nil
}

func (s *LifecycleStore) persist(ctx context.Context) error {
	if s.persistPath == "" {
		return nil
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	records, err := s.snapshotRecords(ctx)
	if err != nil {
		return fmt.Errorf("dercontrol lifecycle persistence: %w", err)
	}
	if err := writeLifecycleEnvelope(s.persistPath, records); err != nil {
		return fmt.Errorf("dercontrol lifecycle persistence: %w", err)
	}
	return nil
}

// Get returns the lifecycle record stored under (parentID, id), or
// store.ErrNotFound.
func (s *LifecycleStore) Get(ctx context.Context, parentID, id string) (LifecycleRecord, error) {
	return s.inner.Get(ctx, parentID, id)
}

// List returns one page of the lifecycle records under parentID.
func (s *LifecycleStore) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[LifecycleRecord], error) {
	return s.inner.List(ctx, parentID, opts)
}

// Count returns the number of lifecycle records under parentID.
func (s *LifecycleStore) Count(ctx context.Context, parentID string) (uint32, error) {
	return s.inner.Count(ctx, parentID)
}

// HasParent reports whether the collection knows parentID.
func (s *LifecycleStore) HasParent(ctx context.Context, parentID string) (bool, error) {
	return s.inner.HasParent(ctx, parentID)
}

// Parents returns the parent ids the collection knows, ascending.
func (s *LifecycleStore) Parents(ctx context.Context) ([]string, error) {
	return s.inner.Parents(ctx)
}

// Create stores a lifecycle record and flushes a snapshot. On a persist
// failure the in-memory write and the key index are rolled back before the
// error returns, matching DERControlStore's contract: Issuer.Issue treats a
// Create failure as possibly-partial and always compensates with a Delete,
// and rollback here closes the window where a reader could see a record that
// never reached disk (acceptance criterion 4).
func (s *LifecycleStore) Create(ctx context.Context, parentID, id string, record LifecycleRecord) error {
	if err := s.inner.Create(ctx, parentID, id, record); err != nil {
		return err
	}
	s.addKey(parentID, id)
	if err := s.persist(ctx); err != nil {
		if derr := s.inner.Delete(ctx, parentID, id); derr != nil && !errors.Is(derr, store.ErrNotFound) {
			return errors.Join(err, fmt.Errorf("dercontrol lifecycle persistence: rollback create %s/%s: %w", parentID, id, derr))
		}
		s.dropKey(parentID, id)
		return err
	}
	return nil
}

// Update replaces a lifecycle record and flushes a snapshot, rolling back to
// the prior value on a persist failure.
func (s *LifecycleStore) Update(ctx context.Context, parentID, id string, record LifecycleRecord) error {
	before, err := s.inner.Get(ctx, parentID, id)
	if err != nil {
		return err
	}
	if err := s.inner.Update(ctx, parentID, id, record); err != nil {
		return err
	}
	if err := s.persist(ctx); err != nil {
		if derr := s.inner.Update(ctx, parentID, id, before); derr != nil {
			return errors.Join(err, fmt.Errorf("dercontrol lifecycle persistence: rollback update %s/%s: %w", parentID, id, derr))
		}
		return err
	}
	return nil
}

// Delete removes a lifecycle record and flushes a snapshot, restoring the
// deleted record on a persist failure.
func (s *LifecycleStore) Delete(ctx context.Context, parentID, id string) error {
	before, err := s.inner.Get(ctx, parentID, id)
	if err != nil {
		return err
	}
	if err := s.inner.Delete(ctx, parentID, id); err != nil {
		return err
	}
	s.dropKey(parentID, id)
	if err := s.persist(ctx); err != nil {
		if derr := s.inner.Create(ctx, parentID, id, before); derr != nil {
			return errors.Join(err, fmt.Errorf("dercontrol lifecycle persistence: rollback delete %s/%s: %w", parentID, id, derr))
		}
		s.addKey(parentID, id)
		return err
	}
	return nil
}

// compile-time proof LifecycleStore satisfies the lifecycleStore interface
// the Issuer consumes, and the full store.ScopedStore contract.
var (
	_ lifecycleStore                     = (*LifecycleStore)(nil)
	_ store.ScopedStore[LifecycleRecord] = (*LifecycleStore)(nil)
)
