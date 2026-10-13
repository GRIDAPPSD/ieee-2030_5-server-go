package adminplane

import (
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// newAdminCommitmentsHandler builds the #801 commitments read handler over
// the process's one ledger, or returns nil, mounting no route, when the
// ledger or the stores that name fleets are not wired.
func newAdminCommitmentsHandler(stores *Stores) *handler.AdminCommitmentsHandler {
	if stores == nil || store.IsAbsent(stores.CommitmentLedger) ||
		store.IsAbsent(stores.EndDevices) || store.IsAbsent(stores.EndDeviceManagers) {
		return nil
	}
	return &handler.AdminCommitmentsHandler{
		Ledger: stores.CommitmentLedger,
		Fleets: commitment.Fleets{Devices: stores.EndDevices, Managers: stores.EndDeviceManagers},
	}
}
