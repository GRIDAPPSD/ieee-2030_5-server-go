package adminplane

import (
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// newAdminDeviceProfileHandler builds the device profile handler, or nil when
// Stores holds no profile store, which leaves the profile routes unmounted.
func newAdminDeviceProfileHandler(stores *Stores) *handler.AdminDeviceProfileHandler {
	if stores == nil || store.IsAbsent(stores.DeviceProfiles) {
		return nil
	}
	return &handler.AdminDeviceProfileHandler{Profiles: stores.DeviceProfiles, Managers: stores.EndDeviceManagers}
}
