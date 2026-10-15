package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// AdminFSAStore is the management-plane store for FunctionSetAssignments
// created via the admin API (GRIDAPPSD/ieee-2030_5-server-go#163). It is
// distinct from the per-device
// scoped FSA store (`ScopedStore[FunctionSetAssignments]`) which is the
// spec-facing surface: an admin FSA is a template that the operator can
// attach programs to and then assign to one or more devices.
//
// Concurrency: every operation is safe for concurrent use. Reads return
// independent copies so callers may mutate without affecting the store.
//
// Per the Pike rule the link bookkeeping (programs attached, devices
// assigned) lives next to the FSA itself rather than as scattered state
// across handlers: one source of truth, one lock domain.
type AdminFSAStore struct {
	mu           sync.RWMutex
	fsas         map[string]sep2.FunctionSetAssignments
	keys         []string                     // sorted for deterministic list ordering
	programLinks map[string][]string          // fsaID -> sorted program hrefs
	deviceLinks  map[string][]string          // fsaID -> sorted device ids
	templates    map[string][]ProgramTemplate // fsaID -> templates sorted by ID

	// Durable persistence (GRIDAPPSD/ieee-2030_5-server-go#165). Empty
	// persistPath = pure in-memory (the historical AdminFSAStore
	// behavior). When set, every mutation
	// flushes a snapshot. A writer holds persistMu across its mutation,
	// the flush and any undo; mu is held only around the in-memory change,
	// so the disk syscall does not block readers.
	persistMu   sync.Mutex
	persistPath string

	// Test hooks, nil in production: afterMutateBeforePersist pauses a
	// writer between its in-memory change and the snapshot, and persistFault
	// fails a snapshot write on demand.
	afterMutateBeforePersist func()
	persistFault             func() error
}

// NewAdminFSAStore returns an empty store.
func NewAdminFSAStore() *AdminFSAStore {
	return &AdminFSAStore{
		fsas:         make(map[string]sep2.FunctionSetAssignments),
		programLinks: make(map[string][]string),
		deviceLinks:  make(map[string][]string),
		templates:    make(map[string][]ProgramTemplate),
	}
}

// persistOrRollback flushes the snapshot and, on failure, runs undo under
// s.mu before returning the persist error, so a write that could not be
// durably recorded leaves the in-memory state exactly as it was
// (GRIDAPPSD/ieee-2030_5-server-go#721): without this, a caller that
// retries after a persist failure (a read-only snapshot directory, for
// example) finds the in-memory mutation already applied, does nothing on
// the retry, and the change never reaches disk at all. undo must not call
// persist itself.
//
// The caller holds persistMu from before its mutation, so no other writer
// can commit, or snapshot, between that mutation and this undo; readers
// still take only mu and are never blocked on the disk write.
func (s *AdminFSAStore) persistOrRollback(undo func()) error {
	if s.afterMutateBeforePersist != nil {
		s.afterMutateBeforePersist()
	}
	if err := s.persistLocked(); err != nil {
		s.mu.Lock()
		undo()
		s.mu.Unlock()
		return err
	}
	return nil
}

// Create persists an admin FSA. Returns ErrAlreadyExists if id is taken.
func (s *AdminFSAStore) Create(_ context.Context, id string, fsa sep2.FunctionSetAssignments) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	if _, exists := s.fsas[id]; exists {
		s.mu.Unlock()
		return store.ErrAlreadyExists
	}
	s.fsas[id] = fsa.Copy()
	idx, _ := slices.BinarySearch(s.keys, id)
	s.keys = slices.Insert(s.keys, idx, id)
	s.mu.Unlock()
	// Persist outside the lock so disk I/O does not block concurrent
	// readers on s.mu.
	return s.persistOrRollback(func() {
		delete(s.fsas, id)
		if idx, found := slices.BinarySearch(s.keys, id); found {
			s.keys = slices.Delete(s.keys, idx, idx+1)
		}
	})
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

// IDs returns the store keys in sorted order. List returns the records
// without them, and a record's key need not equal its mRID.
func (s *AdminFSAStore) IDs(_ context.Context) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.keys)
}

// Delete removes an FSA. ErrNotFound if absent. ErrAdminFSAHasTemplates if
// program templates remain on it, ErrAdminFSAInUse if programs are attached
// or devices assigned: the caller must remove them first; the store does NOT
// silently cascade.
func (s *AdminFSAStore) Delete(_ context.Context, id string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	fsa, exists := s.fsas[id]
	if !exists {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	if len(s.templates[id]) > 0 {
		s.mu.Unlock()
		return ErrAdminFSAHasTemplates
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
	return s.persistOrRollback(func() {
		s.fsas[id] = fsa
		if idx, found := slices.BinarySearch(s.keys, id); !found {
			s.keys = slices.Insert(s.keys, idx, id)
		}
	})
}

// AttachProgram links a DERProgram href to an FSA. ErrNotFound if FSA is
// absent; ErrAlreadyExists if the program is already attached.
func (s *AdminFSAStore) AttachProgram(_ context.Context, fsaID, programHref string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
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
	return s.persistOrRollback(func() {
		hrefs := s.programLinks[fsaID]
		if idx, found := slices.BinarySearch(hrefs, programHref); found {
			s.programLinks[fsaID] = slices.Delete(hrefs, idx, idx+1)
		}
	})
}

// DetachProgram unlinks a DERProgram href from an FSA. ErrNotFound if the
// FSA is absent OR the program is not attached: the caller does not need
// to distinguish (operator UX: "the link is gone either way").
func (s *AdminFSAStore) DetachProgram(_ context.Context, fsaID, programHref string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
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
	return s.persistOrRollback(func() {
		hrefs := s.programLinks[fsaID]
		if idx, found := slices.BinarySearch(hrefs, programHref); !found {
			s.programLinks[fsaID] = slices.Insert(hrefs, idx, programHref)
		}
	})
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
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
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
	return s.persistOrRollback(func() {
		devs := s.deviceLinks[fsaID]
		if idx, found := slices.BinarySearch(devs, deviceID); found {
			s.deviceLinks[fsaID] = slices.Delete(devs, idx, idx+1)
		}
	})
}

// UnassignDevice unlinks a device from an FSA. ErrNotFound if no such link.
func (s *AdminFSAStore) UnassignDevice(_ context.Context, fsaID, deviceID string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
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
	return s.persistOrRollback(func() {
		devs := s.deviceLinks[fsaID]
		if idx, found := slices.BinarySearch(devs, deviceID); !found {
			s.deviceLinks[fsaID] = slices.Insert(devs, idx, deviceID)
		}
	})
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
// Walked once under a read lock: small N, no need for a reverse index yet.
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

// ProgramTemplate is a DERProgram defined on an admin FSA and copied, with
// the same ID and mRID, to every device assigned to that FSA. Every copy
// carries Version, and its DefaultDERControl carries DefaultVersion.
// DefaultMRID is the mRID of each copy's DefaultDERControl, kept here so a
// rebuilt copy keeps its identity.
type ProgramTemplate struct {
	ID                 string              `json:"id"`
	MRID               string              `json:"mRID"`
	Description        string              `json:"description,omitempty"`
	Primacy            uint8               `json:"primacy"`
	Version            uint16              `json:"version"`
	DefaultMRID        string              `json:"defaultMRID,omitempty"`
	DefaultVersion     uint16              `json:"defaultVersion,omitempty"`
	DefaultControlBase sep2.DERControlBase `json:"defaultControlBase"`
}

// Copy returns an independent copy.
func (t ProgramTemplate) Copy() ProgramTemplate {
	c := t
	c.DefaultControlBase = t.DefaultControlBase.Copy()
	return c
}

func compareTemplateID(t ProgramTemplate, id string) int {
	return strings.Compare(t.ID, id)
}

// checkTemplateAdd reports why tmpl cannot join tmpls. Every template of one
// FSA lands on the same devices, so an mRID shared by two of them would name
// two programs on each device. mRIDs are hex, so case is not identity.
func checkTemplateAdd(tmpls []ProgramTemplate, tmpl ProgramTemplate) error {
	if tmpl.ID == "" || tmpl.MRID == "" {
		return fmt.Errorf("%w: id and mRID are required", ErrInvalidProgramTemplate)
	}
	for _, t := range tmpls {
		if t.ID == tmpl.ID || strings.EqualFold(t.MRID, tmpl.MRID) {
			return store.ErrAlreadyExists
		}
	}
	return nil
}

// CreateTemplate adds a program template to an FSA. ErrNotFound if the FSA
// is absent; ErrAlreadyExists if its ID or mRID is taken on that FSA.
func (s *AdminFSAStore) CreateTemplate(_ context.Context, fsaID string, tmpl ProgramTemplate) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	if _, ok := s.fsas[fsaID]; !ok {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	tmpls := s.templates[fsaID]
	if err := checkTemplateAdd(tmpls, tmpl); err != nil {
		s.mu.Unlock()
		return err
	}
	idx, _ := slices.BinarySearchFunc(tmpls, tmpl.ID, compareTemplateID)
	s.templates[fsaID] = slices.Insert(tmpls, idx, tmpl.Copy())
	s.mu.Unlock()
	return s.persistOrRollback(func() {
		tmpls := s.templates[fsaID]
		if idx, found := slices.BinarySearchFunc(tmpls, tmpl.ID, compareTemplateID); found {
			s.setTemplates(fsaID, slices.Delete(tmpls, idx, idx+1))
		}
	})
}

// Template returns an independent copy of one template. ErrNotFound if the
// FSA or the template is absent.
func (s *AdminFSAStore) Template(_ context.Context, fsaID, id string) (ProgramTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tmpls := s.templates[fsaID]
	idx, found := slices.BinarySearchFunc(tmpls, id, compareTemplateID)
	if !found {
		return ProgramTemplate{}, store.ErrNotFound
	}
	return tmpls[idx].Copy(), nil
}

// Templates returns independent copies of the FSA's templates in ID order,
// or an empty slice.
func (s *AdminFSAStore) Templates(_ context.Context, fsaID string) []ProgramTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	tmpls := s.templates[fsaID]
	out := make([]ProgramTemplate, len(tmpls))
	for i, t := range tmpls {
		out[i] = t.Copy()
	}
	return out
}

// UpdateTemplate replaces the template with tmpl.ID. ErrNotFound if the FSA
// or the template is absent. IEEE 2030.5 keeps an object's mRID across
// modifications, so a changed mRID is refused with ErrInvalidProgramTemplate.
func (s *AdminFSAStore) UpdateTemplate(_ context.Context, fsaID string, tmpl ProgramTemplate) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	tmpls := s.templates[fsaID]
	idx, found := slices.BinarySearchFunc(tmpls, tmpl.ID, compareTemplateID)
	if !found {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	before := tmpls[idx]
	if tmpl.MRID != before.MRID {
		s.mu.Unlock()
		return fmt.Errorf("%w: mRID %q cannot change", ErrInvalidProgramTemplate, before.MRID)
	}
	tmpls[idx] = tmpl.Copy()
	s.mu.Unlock()
	return s.persistOrRollback(func() {
		tmpls := s.templates[fsaID]
		if idx, found := slices.BinarySearchFunc(tmpls, before.ID, compareTemplateID); found {
			tmpls[idx] = before
		}
	})
}

// DeleteTemplate removes one template. ErrNotFound if the FSA or the
// template is absent.
func (s *AdminFSAStore) DeleteTemplate(_ context.Context, fsaID, id string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	tmpls := s.templates[fsaID]
	idx, found := slices.BinarySearchFunc(tmpls, id, compareTemplateID)
	if !found {
		s.mu.Unlock()
		return store.ErrNotFound
	}
	before := tmpls[idx]
	s.setTemplates(fsaID, slices.Delete(tmpls, idx, idx+1))
	s.mu.Unlock()
	return s.persistOrRollback(func() {
		tmpls := s.templates[fsaID]
		if idx, found := slices.BinarySearchFunc(tmpls, before.ID, compareTemplateID); !found {
			s.templates[fsaID] = slices.Insert(tmpls, idx, before)
		}
	})
}

// setTemplates stores tmpls under fsaID, dropping the key when it empties so
// Delete's in-use check and the snapshot see no templates. Caller holds s.mu.
func (s *AdminFSAStore) setTemplates(fsaID string, tmpls []ProgramTemplate) {
	if len(tmpls) == 0 {
		delete(s.templates, fsaID)
		return
	}
	s.templates[fsaID] = tmpls
}
