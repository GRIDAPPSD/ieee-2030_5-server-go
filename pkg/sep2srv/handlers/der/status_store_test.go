package der_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// seedFaultTestControl stores one admin-shaped DERControl (an Interval, so
// derivation is attempted) under scope, keyed by id.
func seedFaultTestControl(t *testing.T, controls *memory.ScopedStore[sep2.DERControl], scope, id string) sep2.DERControl {
	t.Helper()
	ctrl := sep2.DERControl{
		DERControlBase: &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: 1}},
	}
	ctrl.Href = "/edev/dev1/fsa/1/derp/1/derc/" + id
	ctrl.Interval = &sep2.DateTimeInterval{Start: 1000, Duration: 900}
	ctrl.CreationTime = 900
	if err := controls.Create(context.Background(), scope, id, ctrl); err != nil {
		t.Fatalf("seed control: %v", err)
	}
	return ctrl
}

// TestDerivedStatusControlStore_LifecycleErrorFailsGetAndList proves the
// fix for a MEDIUM finding on PR 726 (#564): a lifecycle-store error other
// than "no record" must fail the request, not serve the control with no
// EventStatus (an issued control carries none of its own; 2018 EventStatus
// multiplicity is 1, and clients are required to check it before acting).
func TestDerivedStatusControlStore_LifecycleErrorFailsGetAndList(t *testing.T) {
	t.Parallel()

	controls := memory.NewScopedStore[sep2.DERControl]()
	seedFaultTestControl(t, controls, "dev1/1/1", "c1")

	var fault storetest.Fault
	fault.Arm(storetest.ErrBackendUnavailable)
	lifecycles := storetest.NewFaultyScopedStore[dercontrol.LifecycleRecord](memory.NewScopedStore[dercontrol.LifecycleRecord](), &fault)

	decorated := coreder.NewDerivedStatusControlStore(controls, lifecycles)

	if _, err := decorated.Get(context.Background(), "dev1/1/1", "c1"); err == nil {
		t.Error("Get: no error when the lifecycle store errored; the control would be served with no EventStatus")
	} else if !errors.Is(err, storetest.ErrBackendUnavailable) {
		t.Errorf("Get error = %v, want it to wrap storetest.ErrBackendUnavailable", err)
	}

	if _, err := decorated.List(context.Background(), "dev1/1/1", store.ListOptions{Unbounded: true}); err == nil {
		t.Error("List: no error when the lifecycle store errored; the control would be served with no EventStatus")
	} else if !errors.Is(err, storetest.ErrBackendUnavailable) {
		t.Errorf("List error = %v, want it to wrap storetest.ErrBackendUnavailable", err)
	}
}

// TestDerivedStatusControlStore_NotFoundStillServesUnchanged is the control
// for the test above: store.ErrNotFound (no lifecycle record) must NOT fail
// the request, since that is acceptance criterion 2's ordinary case. Proves
// the fix distinguishes "no record" from "a broken store".
func TestDerivedStatusControlStore_NotFoundStillServesUnchanged(t *testing.T) {
	t.Parallel()

	controls := memory.NewScopedStore[sep2.DERControl]()
	seedFaultTestControl(t, controls, "dev1/1/1", "c1")

	lifecycles := memory.NewScopedStore[dercontrol.LifecycleRecord]() // empty: every Get is store.ErrNotFound
	decorated := coreder.NewDerivedStatusControlStore(controls, lifecycles)

	got, err := decorated.Get(context.Background(), "dev1/1/1", "c1")
	if err != nil {
		t.Fatalf("Get: unexpected error on a missing (not broken) lifecycle record: %v", err)
	}
	if got.EventStatus != nil {
		t.Errorf("EventStatus = %+v, want nil (no lifecycle record to derive from)", got.EventStatus)
	}
}
