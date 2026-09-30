package der

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// LifecycleReader is the read-only subset of a DERControl lifecycle store
// this package needs to derive EventStatus at serve time. Defined at this
// consumer per the project's Go standard.
type LifecycleReader interface {
	Get(ctx context.Context, parentID, id string) (dercontrol.LifecycleRecord, error)
}

// DerivedStatusControlStore decorates a DERControl store so every served
// control carries the EventStatus IEEE 2030.5-2018 requires (see
// [dercontrol.DeriveStatus]), computed from its own lifecycle record under
// the SAME (scope, id) pair the control is stored under.
//
// A control with no lifecycle record -- one loaded from a boot fixture or
// the CSIP test loader -- is left exactly as stored: this decorator derives
// no status for a control the issuer did not create, so neither route it
// backs may serve a status this server never asserted for it.
//
// It decorates the LIST and GET routes only; every other method delegates
// straight to controls, unchanged, so the two routes' scoping and paging
// behavior stay exactly what they were before this decorator existed.
type DerivedStatusControlStore struct {
	controls   store.ScopedStore[sep2.DERControl]
	lifecycles LifecycleReader
}

// NewDerivedStatusControlStore decorates controls with lifecycles. Callers
// wire this only when an admin issuer's lifecycle store is present; there is
// no absent-lifecycles arm because the plain, undecorated controls store
// already is that arm.
func NewDerivedStatusControlStore(controls store.ScopedStore[sep2.DERControl], lifecycles LifecycleReader) *DerivedStatusControlStore {
	return &DerivedStatusControlStore{controls: controls, lifecycles: lifecycles}
}

var _ store.ScopedStore[sep2.DERControl] = (*DerivedStatusControlStore)(nil)

// Get returns the control stored under (parentID, id) with its EventStatus
// derived, when a lifecycle record exists for it. A lifecycle lookup
// failure other than "no record" fails the whole request rather than
// serving the control with no status: EventStatus is mandatory (2018
// multiplicity 1, "Clients SHALL verify the EventStatus of an Event before
// acting upon it"), and an issued control carries none of its own, so a
// silently skipped derivation would serve a cancelled control as if it
// carried no status at all rather than as Cancelled.
func (s *DerivedStatusControlStore) Get(ctx context.Context, parentID, id string) (sep2.DERControl, error) {
	ctrl, err := s.controls.Get(ctx, parentID, id)
	if err != nil {
		return ctrl, err
	}
	if err := s.derive(ctx, sep2time.Now().Unix(), parentID, id, &ctrl); err != nil {
		return sep2.DERControl{}, err
	}
	return ctrl, nil
}

// List returns a page of controls, each with its EventStatus derived when a
// lifecycle record exists for it. now is read once for the whole page, not
// once per member, so every control in one response is judged against the
// same instant. A lifecycle lookup failure other than "no record" fails the
// whole list, for the same reason [Get] does.
func (s *DerivedStatusControlStore) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERControl], error) {
	result, err := s.controls.List(ctx, parentID, opts)
	if err != nil {
		return result, err
	}
	now := sep2time.Now().Unix()
	for i := range result.Items {
		id, ok := derhref.ControlID(result.Items[i].Href)
		if !ok {
			continue
		}
		if err := s.derive(ctx, now, parentID, id, &result.Items[i]); err != nil {
			return store.ListResult[sep2.DERControl]{}, err
		}
	}
	return result, nil
}

// derive overwrites ctrl.EventStatus in place from its lifecycle record, or
// leaves ctrl untouched when there is none (acceptance criterion 2). A
// lifecycle lookup failure other than store.ErrNotFound is returned rather
// than swallowed: see [Get].
func (s *DerivedStatusControlStore) derive(ctx context.Context, now int64, parentID, id string, ctrl *sep2.DERControl) error {
	if ctrl.Interval == nil {
		return nil
	}
	lc, err := s.lifecycles.Get(ctx, parentID, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("der: loading lifecycle record for %s/%s: %w", parentID, id, err)
	}
	status := dercontrol.DeriveStatus(now, ctrl.CreationTime, ctrl.Interval.Start, lc)
	ctrl.EventStatus = &status
	return nil
}

func (s *DerivedStatusControlStore) Count(ctx context.Context, parentID string) (uint32, error) {
	return s.controls.Count(ctx, parentID)
}

func (s *DerivedStatusControlStore) HasParent(ctx context.Context, parentID string) (bool, error) {
	return s.controls.HasParent(ctx, parentID)
}

func (s *DerivedStatusControlStore) Parents(ctx context.Context) ([]string, error) {
	return s.controls.Parents(ctx)
}

func (s *DerivedStatusControlStore) Create(ctx context.Context, parentID, id string, resource sep2.DERControl) error {
	return s.controls.Create(ctx, parentID, id, resource)
}

func (s *DerivedStatusControlStore) Update(ctx context.Context, parentID, id string, resource sep2.DERControl) error {
	return s.controls.Update(ctx, parentID, id, resource)
}

func (s *DerivedStatusControlStore) Delete(ctx context.Context, parentID, id string) error {
	return s.controls.Delete(ctx, parentID, id)
}
