package server

import (
	"log"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// newAdminManagementHandler builds the #440 management-pair handler.
// Stores.EndDeviceManagers is the narrower store.EndDeviceManagementStore
// interface (the type the protocol-side ownership gate consults); the
// admin handler needs the concrete *memory.EndDeviceManagementStore for
// RekeyManager/RekeyManaged, which are not part of that interface. Every
// production and test caller wires the concrete type (server.go,
// test_helpers_test.go), so the assertion is expected to hold; a caller
// that wires something else gets no management-pair routes rather than a
// panic, the same nil-means-unmounted shape newAdminFSAHandler uses for a
// nil AdminFSAs.
//
// The two ways this returns nil are not the same condition and are not
// logged the same way. Stores.EndDeviceManagers == nil is a deliberate,
// expected wiring choice (an embedder that provisions pairs some other
// way), so it stays silent. A non-nil store that fails the type assertion
// is a wiring bug: it silently drops create, list, remove AND re-key, not
// only re-key as an earlier PR description said (#677 fix round, H5/LOW-6),
// with a 404 as the only symptom. The protocol-side ownership gate logs the
// comparable condition once at construction (assembly/ownership.go); this
// does the same, at the same "log" package level, rather than staying
// silent where that gate is loud.
func newAdminManagementHandler(stores *Stores) *handler.AdminManagementHandler {
	if stores == nil || stores.EndDeviceManagers == nil {
		return nil
	}
	managers, ok := stores.EndDeviceManagers.(*memory.EndDeviceManagementStore)
	if !ok {
		log.Printf("server: Stores.EndDeviceManagers is %T, not *memory.EndDeviceManagementStore: no management-pair admin routes mounted", stores.EndDeviceManagers)
		return nil
	}
	return &handler.AdminManagementHandler{Managers: managers}
}
