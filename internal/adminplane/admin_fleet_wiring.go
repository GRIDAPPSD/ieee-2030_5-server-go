package adminplane

import (
	"log"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// newAdminFleetHandler builds the #715 fleet-read admin handler. Same
// nil-means-unmounted shape as newAdminManagementHandler: EndDeviceManagers
// unset is a deliberate wiring choice (no management pairs provisioned yet)
// and stays silent, while a non-nil store of the wrong concrete type is a
// wiring bug logged once, because it silently drops every fleet read with a
// 404 as the only symptom.
//
// The optional fields below use store.IsAbsent rather than a plain nil check:
// Stores' concrete *memory.ScopedStore/*memory.Store pointers, assigned
// directly into an interface-typed handler field, produce an interface that
// is non-nil even when the pointer is (see store.IsAbsent's doc comment). A
// deployment that omits DER or mirror storage would otherwise get a fleet
// handler that panics on first read instead of one that reports those
// sections absent.
func newAdminFleetHandler(stores *Stores) *handler.AdminFleetHandler {
	if stores == nil || stores.EndDeviceManagers == nil {
		return nil
	}
	managers, ok := stores.EndDeviceManagers.(*memory.EndDeviceManagementStore)
	if !ok {
		log.Printf("server: Stores.EndDeviceManagers is %T, not *memory.EndDeviceManagementStore: no fleet-read admin route mounted", stores.EndDeviceManagers)
		return nil
	}

	h := &handler.AdminFleetHandler{Managers: managers, Edition: stores.Sep2Edition}
	if !store.IsAbsent(stores.EndDevices) {
		h.EndDevices = stores.EndDevices
	}
	if !store.IsAbsent(stores.DERs) {
		h.DERs = stores.DERs
	}
	if !store.IsAbsent(stores.DERStatuses) {
		h.DERStatuses = stores.DERStatuses
	}
	if !store.IsAbsent(stores.DERAvailabilities) {
		h.DERAvailabilities = stores.DERAvailabilities
	}
	if !store.IsAbsent(stores.MirrorUsagePoints) {
		h.MirrorUsagePoints = stores.MirrorUsagePoints
	}
	if !store.IsAbsent(stores.MirrorMeterReadings) {
		h.MirrorMeterReadings = stores.MirrorMeterReadings
	}
	return h
}
