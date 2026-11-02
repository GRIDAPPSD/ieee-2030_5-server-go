package adminplane

import (
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// newAdminDERReadHandler builds the per-device DER read handler. It needs the
// EndDevice and DER stores to resolve a device and its DERs; the four
// resource stores are optional, and one left unwired is answered as such in
// its own section rather than as an unmounted route.
func newAdminDERReadHandler(stores *Stores) *handler.AdminDERReadHandler {
	if stores == nil || store.IsAbsent(stores.EndDevices) || store.IsAbsent(stores.DERs) {
		return nil
	}
	h := &handler.AdminDERReadHandler{EndDevices: stores.EndDevices, DERs: stores.DERs}
	if !store.IsAbsent(stores.DERStatuses) {
		h.DERStatuses = stores.DERStatuses
	}
	if !store.IsAbsent(stores.DERCapabilities) {
		h.DERCapabilities = stores.DERCapabilities
	}
	if !store.IsAbsent(stores.DERSettings) {
		h.DERSettings = stores.DERSettings
	}
	if !store.IsAbsent(stores.DERAvailabilities) {
		h.DERAvailabilities = stores.DERAvailabilities
	}
	return h
}
