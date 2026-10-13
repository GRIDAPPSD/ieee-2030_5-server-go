package der

import (
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// LifecycleReader is the read-only subset of a DERControl lifecycle store
// the serve-time derivation needs.
type LifecycleReader = dercontrol.LifecycleReader

// DerivedStatusControlStore decorates a DERControl store so every served
// control carries the EventStatus IEEE 2030.5-2018 requires (see
// [dercontrol.DeriveStatus]), computed from its own lifecycle record under
// the SAME (scope, id) pair the control is stored under.
//
// A control with no lifecycle record -- one loaded from a boot fixture or
// the CSIP test loader -- is left exactly as stored: this decorator derives
// no status for a control the issuer did not create, so neither route it
// backs may serve a status this server never asserted for it.
type DerivedStatusControlStore = dercontrol.DerivedStatusStore[sep2.DERControl]

// NewDerivedStatusControlStore decorates controls with lifecycles. Callers
// wire this only when an admin issuer's lifecycle store is present; there is
// no absent-lifecycles arm because the plain, undecorated controls store
// already is that arm.
func NewDerivedStatusControlStore(controls store.ScopedStore[sep2.DERControl], lifecycles LifecycleReader) *DerivedStatusControlStore {
	return NewDerivedStatusControlStoreFor(controls, lifecycles, false)
}

// NewDerivedStatusControlStoreFor is NewDerivedStatusControlStore for a
// server running as the given edition: edition2023 serves
// potentiallySuperseded true on every status, as 2023 requires.
func NewDerivedStatusControlStoreFor(controls store.ScopedStore[sep2.DERControl], lifecycles LifecycleReader, edition2023 bool) *DerivedStatusControlStore {
	return dercontrol.NewDerivedStatusStore(controls, lifecycles, dercontrol.StatusPolicy[sep2.DERControl]{
		Event: func(c *sep2.DERControl) *sep2.Event { return &c.Event },
		ID:    func(_ string, c sep2.DERControl) (string, bool) { return derhref.ControlID(c.Href) },

		AlwaysPotentiallySuperseded: edition2023,
	})
}

var _ store.ScopedStore[sep2.DERControl] = (*DerivedStatusControlStore)(nil)
