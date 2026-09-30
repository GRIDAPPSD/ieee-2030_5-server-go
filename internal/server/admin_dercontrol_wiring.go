package server

import (
	"log"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

// newAdminDERControlHandler builds the #566 DER control handler and the one
// Issuer behind it. Returns nil, mounting no routes, when a store the issuer
// writes is unset. Stores.Responses may be nil: controls then list with zero
// Response counts.
func newAdminDERControlHandler(stores *Stores) *handler.AdminDERControlHandler {
	if stores == nil || stores.DERPrograms == nil || stores.DERControls == nil || stores.DERControlLifecycles == nil || stores.EndDevices == nil {
		return nil
	}
	issuer, err := dercontrol.NewIssuer(stores.DERPrograms, stores.DERControls, stores.DERControlLifecycles, dercontrol.Config{PEN: stores.PEN})
	if err != nil {
		// Unreachable with the default Config bounds used here; logged
		// rather than silently dropping the routes.
		log.Printf("server: DER control issuer: %v: no DER control admin routes mounted", err)
		return nil
	}
	h := &handler.AdminDERControlHandler{
		Issuer:     issuer,
		Controls:   stores.DERControls,
		Lifecycles: stores.DERControlLifecycles,
		Programs:   stores.DERPrograms,
		EndDevices: stores.EndDevices,
		Notifier:   stores.AdminNotifier,
		Ledger:     stores.CommitmentLedger,
		Persisted:  stores.DERControls.Persists() && stores.DERControlLifecycles.Persists(),
	}
	if stores.Responses != nil {
		h.Responses = stores.Responses
	}
	// Without a management store the resolver cannot be built, and a nil
	// Fleets makes every create answer 500 rather than go unchecked.
	if stores.EndDeviceManagers != nil {
		h.Fleets = commitment.Resolver{Devices: stores.EndDevices, Managers: stores.EndDeviceManagers}
	}
	return h
}
