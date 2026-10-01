package server

import (
	"log"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// newAdminDERControlHandler builds the #566 DER control handler and the one
// Issuer behind it. Returns nil, mounting no routes, when a store the issuer
// writes is unset. Stores.Responses may be nil: controls then list with zero
// Response counts.
func newAdminDERControlHandler(stores *Stores) *handler.AdminDERControlHandler {
	if stores == nil || stores.DERPrograms == nil || stores.DERControls == nil || stores.DERControlLifecycles == nil || stores.EndDevices == nil {
		return nil
	}
	// Run supplies the process's one issuer; a Stores built without one gets
	// its own, which only tests do.
	issuer := stores.DERControlIssuer
	if issuer == nil {
		var err error
		issuer, err = assembly.NewDERControlIssuer(stores.DERPrograms, stores.DERControls, stores.DERControlLifecycles, stores.PEN)
		if err != nil {
			// Unreachable with the default Config bounds used here; logged
			// rather than silently dropping the routes.
			log.Printf("server: DER control issuer: %v: no DER control admin routes mounted", err)
			return nil
		}
	}
	h := &handler.AdminDERControlHandler{
		Issuer:     issuer,
		Controls:   stores.DERControls,
		Lifecycles: stores.DERControlLifecycles,
		Programs:   stores.DERPrograms,
		EndDevices: stores.EndDevices,
		Notifier:   stores.AdminNotifier,
		Persisted:  stores.DERControls.Persists() && stores.DERControlLifecycles.Persists(),
	}
	if stores.Responses != nil {
		h.Responses = stores.Responses
	}
	// A missing management store or ledger leaves the handler's field nil,
	// so every create answers 500 rather than going unchecked; a cancel still
	// runs, without the fleet lock, and logs a warning. IsAbsent
	// also catches a typed-nil pointer, which a plain nil test passes.
	if store.IsAbsent(stores.EndDeviceManagers) {
		log.Printf("server: DER control create: no EndDevice management store; every create answers 500 and a cancel runs without the fleet lock")
	} else {
		h.Fleets = commitment.Resolver{Devices: stores.EndDevices, Managers: stores.EndDeviceManagers}
	}
	if store.IsAbsent(stores.CommitmentLedger) {
		log.Printf("server: DER control create: no commitment ledger; every create answers 500 and a cancel runs without the fleet lock")
	} else {
		h.Ledger = stores.CommitmentLedger
	}
	return h
}
