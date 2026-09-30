package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// TestNewRunStores_BuildsTheLedgerOverItsOwnStores: the stores Run serves
// carry a commitment ledger, and it reads Run's own response store, so a
// grant stored there holds its fleet's window. Without it every windowed
// flow reservation grant in production is refused.
func TestNewRunStores_BuildsTheLedgerOverItsOwnStores(t *testing.T) {
	ctx := context.Background()
	stores, _, err := newRunStores(&config.Config{})
	if err != nil {
		t.Fatalf("newRunStores: %v", err)
	}
	if stores.CommitmentLedger == nil {
		t.Fatal("CommitmentLedger is nil: every windowed grant would be refused")
	}

	lfdi := strings.Repeat("A", 40)
	if err := stores.EndDevices.Create(ctx, "e1", sep2.EndDevice{LFDI: lfdi}); err != nil {
		t.Fatal(err)
	}
	frp := sep2.FlowReservationResponse{}
	frp.Href = "/edev/e1/frp/R1"
	frp.MRID = "MRID-R1"
	frp.Interval = &sep2.DateTimeInterval{Start: 1000, Duration: 600}
	if err := stores.FlowReservationResponses.Create(ctx, "e1", "R1", frp); err != nil {
		t.Fatal(err)
	}

	w := commitment.Window{Start: 1200, Duration: 10}
	err = stores.CommitmentLedger.Within(ctx, []string{lfdi}, func(v commitment.View) error {
		return v.CheckGrant(ctx, lfdi, &w, "")
	})
	var ce *commitment.ConflictError
	if !errors.As(err, &ce) || ce.MRID != "MRID-R1" {
		t.Fatalf("CheckGrant over Run's stores = %v, want a conflict naming MRID-R1", err)
	}
}
