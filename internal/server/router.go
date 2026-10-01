package server

import (
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
)

// Stores holds all resource stores for the server.
type Stores = adminplane.Stores

// NewCommitmentLedger builds the commitment ledger over s's own stores.
func NewCommitmentLedger(s *Stores) *commitment.Ledger {
	return adminplane.NewCommitmentLedger(s)
}
