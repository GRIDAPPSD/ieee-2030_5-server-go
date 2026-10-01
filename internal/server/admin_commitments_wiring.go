package server

import (
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// newAdminCommitmentsHandler builds the #801 commitments read handler over
// the process's one ledger, or returns nil, mounting no route, when none is
// wired.
func newAdminCommitmentsHandler(stores *Stores) *handler.AdminCommitmentsHandler {
	if stores == nil || store.IsAbsent(stores.CommitmentLedger) {
		return nil
	}
	return &handler.AdminCommitmentsHandler{Ledger: stores.CommitmentLedger}
}
