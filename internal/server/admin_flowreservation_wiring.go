package server

import (
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// newAdminFlowReservationHandler builds the #764 flow reservation read
// handler. It returns nil, mounting no routes, when a store a view needs is
// unset, like the other DERMS read handlers.
//
// Responses and controls are wrapped the way the protocol routes wrap them,
// so a view's EventStatus is the one a device would read. The DER control
// stores are optional: without them no control exists and every response
// lists no execution.
func newAdminFlowReservationHandler(stores *Stores) *handler.AdminFlowReservationHandler {
	if stores == nil ||
		store.IsAbsent(stores.FlowReservationRequests) || store.IsAbsent(stores.FlowReservationResponses) ||
		store.IsAbsent(stores.FlowReservationResponseLifecycles) ||
		store.IsAbsent(stores.EndDevices) || store.IsAbsent(stores.EndDeviceManagers) {
		return nil
	}
	fleets := commitment.Resolver{Devices: stores.EndDevices, Managers: stores.EndDeviceManagers}
	h := &handler.AdminFlowReservationHandler{
		Requests:   stores.FlowReservationRequests,
		Responses:  flowreservation.NewDerivedStatusResponseStore(stores.FlowReservationResponses, stores.FlowReservationResponseLifecycles),
		Lifecycles: stores.FlowReservationResponseLifecycles,
		Fleets:     fleets,
		Deadline:   flowReservationConfig(stores.FlowReservationDeadline),
		Persisted:  persists(stores.FlowReservationRequests) && persists(stores.FlowReservationResponses) && persists(stores.FlowReservationResponseLifecycles),
	}
	if !store.IsAbsent(stores.DERControls) && !store.IsAbsent(stores.DERControlLifecycles) {
		h.Controls = coreder.NewDerivedStatusControlStore(stores.DERControls, stores.DERControlLifecycles)
		h.Executions = sources.NewControls(stores.DERControls, stores.DERControlLifecycles, fleets)
	}
	return h
}

// persists reports whether a store keeps its records across a restart. A
// store with no such method (the in-memory one) does not.
func persists(s any) bool {
	p, ok := s.(interface{ Persists() bool })
	return ok && p.Persists()
}
