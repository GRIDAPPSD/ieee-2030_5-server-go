package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

// IEEE-097: durable RegistrationStore persistence.
//
// Today Registrations live in a bare *Store[sep2.Registration] on
// server.Stores. RegistrationStore is a thin wrapper that adds on-disk
// snapshots while keeping the underlying Store's interface intact — call
// sites that take a *memory.Store[sep2.Registration] keep compiling
// because RegistrationStore embeds it.
//
// The wrapper carries a private persistMu + persistPath so the embedded
// Store stays exactly as it was pre-IEEE-097.

// RegistrationStore wraps Store[sep2.Registration] with disk persistence.
// Embed-and-override: Create/Update/Delete shadow the embedded methods so
// they can flush a snapshot after a successful in-memory mutation.
type RegistrationStore struct {
	*Store[sep2.Registration]

	persistMu   sync.Mutex
	persistPath string
}

// registrationRecord is the on-disk record shape for the registration
// snapshot. Mirrors endDeviceRecord — keyed by the store key, value is
// the resource itself.
type registrationRecord struct {
	ID           string            `json:"id"`
	Registration sep2.Registration `json:"registration"`
}

// NewRegistrationStore creates an in-memory RegistrationStore (no
// persistence). The historical pre-IEEE-097 callers can switch from
// memory.NewStore[sep2.Registration]() to NewRegistrationStore() when
// they want symmetry across the admin stores; both behave identically
// when no persistence path is wired.
func NewRegistrationStore() *RegistrationStore {
	return &RegistrationStore{
		Store: NewStore[sep2.Registration](),
	}
}

// NewRegistrationStoreWithPersistence builds a RegistrationStore wired to
// a JSON file. Empty path = in-memory. Missing file = cold boot.
// Corrupt/version-unknown = error.
func NewRegistrationStoreWithPersistence(path string) (*RegistrationStore, error) {
	store := NewRegistrationStore()
	if path == "" {
		return store, nil
	}
	if err := store.loadFromFile(path); err != nil {
		return nil, fmt.Errorf("registration persistence: load %q: %w", path, err)
	}
	store.persistPath = path
	return store, nil
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
		if err := s.Store.Create(ctx, r.ID, r.Registration); err != nil {
			return fmt.Errorf("rehydrate %q: %w", r.ID, err)
		}
	}
	return nil
}

func (s *RegistrationStore) snapshotRecords() []registrationRecord {
	s.Store.mu.RLock()
	defer s.Store.mu.RUnlock()
	out := make([]registrationRecord, 0, len(s.Store.keys))
	for _, k := range s.Store.keys {
		out = append(out, registrationRecord{
			ID:           k,
			Registration: s.Store.data[k].Copy(),
		})
	}
	return out
}

func (s *RegistrationStore) persist() error {
	if s.persistPath == "" {
		return nil
	}
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	records := s.snapshotRecords()
	if err := writeSnapshotEnvelope(s.persistPath, records); err != nil {
		return fmt.Errorf("registration persistence: %w", err)
	}
	return nil
}

// Create persists a registration. Shadows Store.Create — same return
// values; only difference is the disk flush at the end.
func (s *RegistrationStore) Create(ctx context.Context, id string, reg sep2.Registration) error {
	if err := s.Store.Create(ctx, id, reg); err != nil {
		return err
	}
	return s.persist()
}

// Update persists a registration update.
func (s *RegistrationStore) Update(ctx context.Context, id string, reg sep2.Registration) error {
	if err := s.Store.Update(ctx, id, reg); err != nil {
		return err
	}
	return s.persist()
}

// Delete persists a registration removal.
func (s *RegistrationStore) Delete(ctx context.Context, id string) error {
	if err := s.Store.Delete(ctx, id); err != nil {
		return err
	}
	return s.persist()
}
