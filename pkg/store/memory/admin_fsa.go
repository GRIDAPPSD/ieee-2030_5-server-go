package memory

import (
	"context"
	"slices"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
)

// AdminFSAStore is the management-plane store for FunctionSetAssignments
// created via the admin API (IEEE-096). It is distinct from the per-device
// scoped FSA store (`ScopedStore[FunctionSetAssignments]`) which is the
// spec-facing surface — an admin FSA is a template that the operator can
// attach programs to and then assign to one or more devices.
//
// Concurrency: every operation is safe for concurrent use. Reads return
// independent copies so callers may mutate without affecting the store.
//
// Per the Pike rule the link bookkeeping (programs attached, devices
// assigned) lives next to the FSA itself rather than as scattered state
// across handlers — one source of truth, one lock domain.
type AdminFSAStore struct {
	mu           sync.RWMutex
	fsas         map[string]sep2.FunctionSetAssignments
	keys         []string // sorted for deterministic list ordering
	programLinks map[string][]string // fsaID -> sorted program hrefs
	deviceLinks  map[string][]string // fsaID -> sorted device ids

	// IEEE-097: durable persistence. Empty persistPath = pure in-memory
	// (the historical AdminFSAStore behavior). When set, every mutation
	// flushes a snapshot under persistMu. Held separately from mu so the
	// disk syscall does not block readers/writers on the in-memory state.
	persistMu   sync.Mutex
	persistPath string
}

// NewAdminFSAStore returns an empty store.
func NewAdminFSAStore() *AdminFSAStore {
	return &AdminFSAStore{
		fsas:         make(map[string]sep2.FunctionSetAssignments),
		programLinks: make(map[string][]string),
		deviceLinks:  make(map[string][]string),
	}
}

// Create persists an admin FSA. Returns ErrAlreadyExists if id is taken.
func (s *AdminFSAStore) Create(_ context.Context, id string, fsa sep2.FunctionSetAssignments) error {
	s.mu.Lock()
	if _, exists := s.fsas[id]; exists {
		s.mu.Unlock()
		return store.ErrAlreadyExists
	}
	s.fsas[id] = fsa.Copy()
	idx, _ := slices.BinarySearch(s.keys, id)
	s.keys = slices.Insert(s.keys, idx, id)
	s.mu.Unlock()
	// IEEE-097: persist outside the lock so disk I/O does not block
	// concurrent readers on s.mu.
	return s.persist()
}

// Get returns an independent copy of the FSA. ErrNotFound if absent.
func (s *AdminFSAStore) Get(_ context.Context, id string) (sep2.FunctionSetAssignments, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fsa, ok := s.fsas[id]
	if !ok {
		return sep2.FunctionSetAssignments{}, store.ErrNotFound
	}
	return fsa.Copy(), nil
}

// List returns all FSAs in key order. Independent copies.
func (s *AdminFSAStore) List(_ context.Context) []sep2.FunctionSetAssignments {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]sep2.FunctionSetAssignments, 0, len(s.keys))
	for _, k := range s.keys {
		out = append(out, s.fsas[k].Copy())
	}
	return out
}

// Delete removes an FSA. ErrNotFound if absent. ErrInUse if the FSA still
// has programs attached or devices assigned — the caller must detach/unlink
// first; the store does NOT silently cascade.
func (s *AdminFSAStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	if _, exists := s.fsas[id]; !exists {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	if len(s.programLinks[id]) > 0 || len(s.deviceLinks[id]) > 0 {
		s.mu.Unlock()
		return ErrAdminFSAInUse
	}
	delete(s.fsas, id)
	if idx, found := slices.BinarySearch(s.keys, id); found {
		s.keys = slices.Delete(s.keys, idx, idx+1)
	}
	s.mu.Unlock()
	return s.persist()
}

// AttachProgram links a DERProgram href to an FSA. ErrNotFound if FSA is
// absent; ErrAlreadyExists if the program is already attached.
func (s *AdminFSAStore) AttachProgram(_ context.Context, fsaID, programHref string) error {
	s.mu.Lock()
	if _, ok := s.fsas[fsaID]; !ok {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	hrefs := s.programLinks[fsaID]
	idx, found := slices.BinarySearch(hrefs, programHref)
	if found {
		s.mu.Unlock()
		return store.ErrAlreadyExists
	}
	s.programLinks[fsaID] = slices.Insert(hrefs, idx, programHref)
	s.mu.Unlock()
	return s.persist()
}

// DetachProgram unlinks a DERProgram href from an FSA. ErrNotFound if the
// FSA is absent OR the program is not attached — the caller does not need
// to distinguish (operator UX: "the link is gone either way").
func (s *AdminFSAStore) DetachProgram(_ context.Context, fsaID, programHref string) error {
	s.mu.Lock()
	if _, ok := s.fsas[fsaID]; !ok {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	hrefs := s.programLinks[fsaID]
	idx, found := slices.BinarySearch(hrefs, programHref)
	if !found {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	s.programLinks[fsaID] = slices.Delete(hrefs, idx, idx+1)
	s.mu.Unlock()
	return s.persist()
}

// Programs returns sorted program hrefs attached to the FSA, or empty slice.
func (s *AdminFSAStore) Programs(_ context.Context, fsaID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	hrefs := s.programLinks[fsaID]
	out := make([]string, len(hrefs))
	copy(out, hrefs)
	return out
}

// AssignDevice links a device id to an FSA. ErrNotFound if FSA absent;
// ErrAlreadyExists if the device is already assigned.
func (s *AdminFSAStore) AssignDevice(_ context.Context, fsaID, deviceID string) error {
	s.mu.Lock()
	if _, ok := s.fsas[fsaID]; !ok {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	devs := s.deviceLinks[fsaID]
	idx, found := slices.BinarySearch(devs, deviceID)
	if found {
		s.mu.Unlock()
		return store.ErrAlreadyExists
	}
	s.deviceLinks[fsaID] = slices.Insert(devs, idx, deviceID)
	s.mu.Unlock()
	return s.persist()
}

// UnassignDevice unlinks a device from an FSA. ErrNotFound if no such link.
func (s *AdminFSAStore) UnassignDevice(_ context.Context, fsaID, deviceID string) error {
	s.mu.Lock()
	if _, ok := s.fsas[fsaID]; !ok {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	devs := s.deviceLinks[fsaID]
	idx, found := slices.BinarySearch(devs, deviceID)
	if !found {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	s.deviceLinks[fsaID] = slices.Delete(devs, idx, idx+1)
	s.mu.Unlock()
	return s.persist()
}

// Devices returns sorted device ids assigned to the FSA, or empty slice.
func (s *AdminFSAStore) Devices(_ context.Context, fsaID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	devs := s.deviceLinks[fsaID]
	out := make([]string, len(devs))
	copy(out, devs)
	return out
}

// FSAsForDevice returns sorted FSA ids assigned to the given device.
// Walked once under a read lock — small N, no need for a reverse index yet.
func (s *AdminFSAStore) FSAsForDevice(_ context.Context, deviceID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for fsaID, devs := range s.deviceLinks {
		if _, found := slices.BinarySearch(devs, deviceID); found {
			out = append(out, fsaID)
		}
	}
	slices.Sort(out)
	return out
}
