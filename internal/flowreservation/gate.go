package flowreservation

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
)

// ErrCommitmentCheck marks a grant refused because the commitment check
// could not complete (a store read, the fleet lookup, an unwired ledger).
// It is an internal error, never a conflict: nothing was found in the way.
var ErrCommitmentCheck = errors.New("flowreservation: commitment check failed")

// Gate runs a grant's store write only when its window is free on the
// fleet edevID belongs to (#714): one commitment per fleet window.
type Gate interface {
	// Grant calls write, under the fleet's commitment lock, only when a
	// grant on w is free, and returns a *commitment.ConflictError
	// otherwise. except names a grant being replaced ("" for a first
	// answer).
	Grant(ctx context.Context, edevID string, w *sep2.DateTimeInterval, except string, write func(ctx context.Context) error) error
}

// FleetResolver maps an EndDevice id to its fleet key;
// commitment.Resolver is the production one.
type FleetResolver interface {
	FleetOf(ctx context.Context, endDeviceID string) (string, error)
}

// LedgerGate is the production Gate: it resolves the fleet, then checks and
// writes inside one commitment.Ledger.Within call, so no other grant or
// control of the fleet can commit between the check and the write.
type LedgerGate struct {
	ledger *commitment.Ledger
	fleets FleetResolver
}

// NewLedgerGate builds a LedgerGate. A nil ledger refuses every grant
// (commitment.ErrNoLedger); fleets is required.
func NewLedgerGate(ledger *commitment.Ledger, fleets FleetResolver) *LedgerGate {
	if fleets == nil {
		panic("flowreservation: NewLedgerGate: fleets must not be nil")
	}
	return &LedgerGate{ledger: ledger, fleets: fleets}
}

// Grant implements Gate.
func (g *LedgerGate) Grant(ctx context.Context, edevID string, w *sep2.DateTimeInterval, except string, write func(ctx context.Context) error) error {
	if g.ledger == nil {
		return commitment.ErrNoLedger
	}
	fleet, err := g.fleets.FleetOf(ctx, edevID)
	if err != nil {
		return fmt.Errorf("flowreservation: fleet of %s: %w", edevID, err)
	}
	var window *commitment.Window
	if w != nil {
		window = &commitment.Window{Start: w.Start, Duration: w.Duration}
	}
	return g.ledger.Within(ctx, []string{fleet}, func(v commitment.View) error {
		if err := v.CheckGrant(ctx, fleet, window, except); err != nil {
			return err
		}
		return write(ctx)
	})
}
