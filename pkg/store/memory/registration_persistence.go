package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Durable RegistrationStore persistence
// (GRIDAPPSD/ieee-2030_5-server-go#165).
//
// RegistrationStore adds on-disk snapshots to a Registration collection: every
// mutation that succeeds against the collection is followed by a whole-file
// snapshot write, so a restart rehydrates what the server last committed.
//
// # It holds the collection, it does not embed it
//
// This wrapper used to embed *Store[sep2.Registration] and rely on method
// promotion for Get, List and Count while shadowing the three mutators. That
// made the wrapper's conformance to [store.ResourceStore] an accident of what
// the embedded concrete type happened to provide: a method added to the
// contract would have been answered silently by the inner store, with no flush
// and no compile error, which is the quiet drift the contract exists to
// prevent.
//
// It now holds the collection as a [store.ResourceStore] field and forwards
// every method explicitly. Nothing is promoted, so the compile-time assertion
// in contract.go is a real proof rather than a restatement of the embedding.
//
// # Why the wrapper keeps its own key list
//
// A snapshot is a list of (id, resource) pairs, and the contract has no key
// enumeration: List hands back resources, not the keys they are stored under.
// Recovering an id from a field of the resource is not an option, because that
// makes the durable identity of a record depend on a value a writer controls
// rather than on the key the collection is actually indexed by. A
// wrong-but-plausible key writes a snapshot that rehydrates the fleet under
// different addresses, with nothing failing at the time it is written.
//
// So the wrapper records the ids it writes. That is sound because the wrapper
// OWNS its inner collection: it is constructed here and no other handle to it
// exists. This is why the constructors below build the inner store rather than
// accepting one; a shared collection wired in behind this wrapper would produce
// snapshots that omit whatever was written around it.
type RegistrationStore struct {
	// inner is the collection this wrapper owns. Declared as the contract, so
	// a change to the contract fails the build here rather than being absorbed
	// by an embedded implementation.
	inner store.ResourceStore[sep2.Registration]

	// keysMu guards keys, the ids written through this wrapper, held ascending
	// so the snapshot's record order matches the order List serves.
	keysMu sync.Mutex
	keys   []string

	persistMu   sync.Mutex
	persistPath string
}

// registrationRecord is the on-disk record shape for the registration
// snapshot. Keyed by the store key, value is the resource itself.
type registrationRecord struct {
	ID           string            `json:"id"`
	Registration sep2.Registration `json:"registration"`
}

// NewRegistrationStore creates an in-memory RegistrationStore (no
// persistence). It behaves exactly as a bare Store[sep2.Registration] does
// when no persistence path is wired.
func NewRegistrationStore() *RegistrationStore {
	return &RegistrationStore{inner: NewStore[sep2.Registration]()}
}

// NewRegistrationStoreWithPersistence builds a RegistrationStore wired to
// a JSON file. Empty path = in-memory. Missing file = cold boot.
// Corrupt/version-unknown = error.
func NewRegistrationStoreWithPersistence(path string) (*RegistrationStore, error) {
	s := NewRegistrationStore()
	if path == "" {
		return s, nil
	}
	if err := s.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("registration persistence: load %q: %w", path, err)
	}
	s.persistPath = path
	return s, nil
}

func (s *RegistrationStore) loadFromFile(path string) error {
	env, err := readSnapshotEnvelope(path)
	if err != nil {
		return err
	}
	if env == nil || len(env.Records) == 0 {
		return nil
	}
	var records []registrationRecord
	if err := json.Unmarshal(env.Records, &records); err != nil {
		return fmt.Errorf("decode records: %w", err)
	}
	ctx := context.Background()
	for _, r := range records {
		// Rehydration goes through this wrapper's own Create so the key list
		// is populated by the same path that maintains it during service.
		// persistPath is still empty at this point, so reading the snapshot
		// cannot write one back.
		if err := s.Create(ctx, r.ID, r.Registration); err != nil {
			return fmt.Errorf("rehydrate %q: %w", r.ID, err)
		}
	}
	return nil
}

// addKey records id as present, keeping keys sorted ascending.
func (s *RegistrationStore) addKey(id string) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	if idx, found := slices.BinarySearch(s.keys, id); !found {
		s.keys = slices.Insert(s.keys, idx, id)
	}
}

// dropKey records id as absent.
func (s *RegistrationStore) dropKey(id string) {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	if idx, found := slices.BinarySearch(s.keys, id); found {
		s.keys = slices.Delete(s.keys, idx, idx+1)
	}
}

// snapshotKeys returns an independent copy of the current key list.
func (s *RegistrationStore) snapshotKeys() []string {
	s.keysMu.Lock()
	defer s.keysMu.Unlock()
	return slices.Clone(s.keys)
}

// snapshotRecords reads back every recorded id and emits the record list.
//
// A key that has gone missing between the copy and the read is skipped: a
// concurrent Delete removed it, and the snapshot that Delete is about to write
// is the one that describes that state. Any OTHER error aborts the snapshot,
// so a collection that could not be read fails the write rather than
// committing a file that silently drops records: a short snapshot is
// indistinguishable on disk from a legitimate deletion.
func (s *RegistrationStore) snapshotRecords(ctx context.Context) ([]registrationRecord, error) {
	keys := s.snapshotKeys()
	out := make([]registrationRecord, 0, len(keys))
	for _, k := range keys {
		reg, err := s.inner.Get(ctx, k)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("read %q for snapshot: %w", k, err)
		}
		out = append(out, registrationRecord{ID: k, Registration: reg})
	}
	return out, nil
}

func (s *RegistrationStore) persist(ctx context.Context) error {
	if s.persistPath == "" {
		return nil
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	records, err := s.snapshotRecords(ctx)
	if err != nil {
		return fmt.Errorf("registration persistence: %w", err)
	}
	if err := writeSnapshotEnvelope(s.persistPath, records); err != nil {
		return fmt.Errorf("registration persistence: %w", err)
	}
	return nil
}

// Get returns the registration stored under id, or store.ErrNotFound.
func (s *RegistrationStore) Get(ctx context.Context, id string) (sep2.Registration, error) {
	return s.inner.Get(ctx, id)
}

// List returns one page of registrations.
func (s *RegistrationStore) List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.Registration], error) {
	return s.inner.List(ctx, opts)
}

// Count returns the number of stored registrations.
func (s *RegistrationStore) Count(ctx context.Context) (uint32, error) {
	return s.inner.Count(ctx)
}

// Create stores a registration and flushes a snapshot.
func (s *RegistrationStore) Create(ctx context.Context, id string, reg sep2.Registration) error {
	if err := s.inner.Create(ctx, id, reg); err != nil {
		return err
	}
	s.addKey(id)
	return s.persist(ctx)
}

// Update replaces a registration and flushes a snapshot.
func (s *RegistrationStore) Update(ctx context.Context, id string, reg sep2.Registration) error {
	if err := s.inner.Update(ctx, id, reg); err != nil {
		return err
	}
	return s.persist(ctx)
}

// Delete removes a registration and flushes a snapshot.
func (s *RegistrationStore) Delete(ctx context.Context, id string) error {
	if err := s.inner.Delete(ctx, id); err != nil {
		return err
	}
	s.dropKey(id)
	return s.persist(ctx)
}
