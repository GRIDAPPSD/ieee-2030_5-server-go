package adminplane

import (
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// newAdminControlEventsHandler builds the #1190 control-events read handler.
// It returns nil, mounting no route, when any store it reads is unset: the
// route has to see both device and FSA controls and the management pairs to
// place each control in a lane, so a partial answer is not offered.
func newAdminControlEventsHandler(stores *Stores) *handler.AdminControlEventsHandler {
	if stores == nil || stores.DERControls == nil || store.IsAbsent(stores.DERControlLifecycles) || stores.EndDevices == nil ||
		stores.AdminFSAs == nil || store.IsAbsent(stores.EndDeviceManagers) {
		return nil
	}
	// The fleet handler is nil, and logged once, for a store of another type.
	fleet := newAdminFleetHandler(stores)
	if fleet == nil {
		return nil
	}
	return &handler.AdminControlEventsHandler{
		Controls:   stores.DERControls,
		Lifecycles: stores.DERControlLifecycles,
		EndDevices: stores.EndDevices,
		FSAs:       stores.AdminFSAs,
		Fleet:      fleet,
	}
}
