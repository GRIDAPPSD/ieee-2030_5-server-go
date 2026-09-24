package server

import (
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
func newAdminManagementHandler(stores *Stores) *handler.AdminManagementHandler {
	if stores == nil || stores.EndDeviceManagers == nil {
		return nil
	}
	managers, ok := stores.EndDeviceManagers.(*memory.EndDeviceManagementStore)
	if !ok {
		return nil
	}
	return &handler.AdminManagementHandler{Managers: managers}
}
