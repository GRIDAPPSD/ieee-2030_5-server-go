package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ProgramOriginKind names how a runtime DERProgram came to exist.
type ProgramOriginKind string

const (
	// ProgramOriginTemplate is a device's copy of a template on an admin FSA.
	ProgramOriginTemplate ProgramOriginKind = "template"
	// ProgramOriginDevice is a program created directly under one device FSA.
	ProgramOriginDevice ProgramOriginKind = "device"
)

// ErrInvalidProgramOrigin is returned for an origin the index will not hold.
var ErrInvalidProgramOrigin = errors.New("invalid program origin")

// ProgramOrigin records where one device's DERProgram came from. A program
// with no origin entry is a fixture or external program, so there is no
// fixture kind.
type ProgramOrigin struct {
	Kind ProgramOriginKind `json:"kind"`
	// FSAID is the FSA segment of the program's href.
	FSAID string `json:"fsaID"`
	// TemplateID names the admin FSA template a copy came from; set for
	// template copies only.
	TemplateID string `json:"templateID,omitempty"`
}

// Copy returns an independent copy.
func (o ProgramOrigin) Copy() ProgramOrigin { return o }

// Validate reports whether o is an origin the index may hold.
func (o ProgramOrigin) Validate() error {
	if o.FSAID == "" {
		return fmt.Errorf("%w: fsaID is required", ErrInvalidProgramOrigin)
	}
	switch o.Kind {
	case ProgramOriginTemplate:
		if o.TemplateID == "" {
			return fmt.Errorf("%w: a template copy needs its templateID", ErrInvalidProgramOrigin)
		}
	case ProgramOriginDevice:
		if o.TemplateID != "" {
			return fmt.Errorf("%w: a device program has no templateID", ErrInvalidProgramOrigin)
		}
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidProgramOrigin, o.Kind)
	}
	return nil
}

// UnmarshalJSON refuses an origin Validate refuses, so a snapshot cannot load
// a record the write path would have rejected and misclassify a program.
func (o *ProgramOrigin) UnmarshalJSON(data []byte) error {
	type plain ProgramOrigin
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	if err := ProgramOrigin(p).Validate(); err != nil {
		return err
	}
	*o = ProgramOrigin(p)
	return nil
}

// ProgramOriginStore is the persisted origin index, keyed (device id, program
// id) like the DERProgram store. Every write is validated first.
type ProgramOriginStore struct {
	inner *PersistentScopedStore[ProgramOrigin]
}

// NewProgramOriginStore builds the index over a JSON snapshot at path and
// loads it. An empty path is pure in-memory.
func NewProgramOriginStore(path string) (*ProgramOriginStore, error) {
	inner, err := NewPersistentScopedStore[ProgramOrigin](path, "ProgramOrigin")
	if err != nil {
		return nil, err
	}
	return &ProgramOriginStore{inner: inner}, nil
}

// Persists reports whether the index is wired to a snapshot path.
func (s *ProgramOriginStore) Persists() bool { return s.inner.Persists() }

// Get returns the origin of one program, or store.ErrNotFound when it has
// none.
func (s *ProgramOriginStore) Get(ctx context.Context, deviceID, programID string) (ProgramOrigin, error) {
	return s.inner.Get(ctx, deviceID, programID)
}

// Create records a program's origin. store.ErrAlreadyExists if it has one.
func (s *ProgramOriginStore) Create(ctx context.Context, deviceID, programID string, o ProgramOrigin) error {
	if err := o.Validate(); err != nil {
		return err
	}
	return s.inner.Create(ctx, deviceID, programID, o)
}

// Update replaces a program's origin. store.ErrNotFound if it has none.
func (s *ProgramOriginStore) Update(ctx context.Context, deviceID, programID string, o ProgramOrigin) error {
	if err := o.Validate(); err != nil {
		return err
	}
	return s.inner.Update(ctx, deviceID, programID, o)
}

// Delete removes a program's origin. store.ErrNotFound if it has none.
func (s *ProgramOriginStore) Delete(ctx context.Context, deviceID, programID string) error {
	return s.inner.Delete(ctx, deviceID, programID)
}
