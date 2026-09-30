package csiptest_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// TestNewFreshStores_BuildsTheLedgerLikeRun: the harness stores carry the
// commitment ledger and both lifecycle stores Run builds, and the ledger
// reads the harness's own response and cancel-mark stores. Without them a
// windowed flow reservation grant is refused and a "denied when committed"
// test would pass vacuously.
func TestNewFreshStores_BuildsTheLedgerLikeRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := csiptest.NewFreshStores()
	if s.CommitmentLedger == nil || s.DERControlLifecycles == nil || s.FlowReservationResponseLifecycles == nil {
		t.Fatalf("ledger %v, control lifecycles %v, response lifecycles %v; want all three",
			s.CommitmentLedger != nil, s.DERControlLifecycles != nil, s.FlowReservationResponseLifecycles != nil)
	}

	lfdi := strings.Repeat("A", 40)
	if err := s.EndDevices.Create(ctx, "e1", sep2.EndDevice{LFDI: lfdi}); err != nil {
		t.Fatal(err)
	}
	frp := sep2.FlowReservationResponse{}
	frp.Href = "/edev/e1/frp/R1"
	frp.MRID = "MRID-R1"
	frp.Interval = &sep2.DateTimeInterval{Start: 1000, Duration: 600}
	if err := s.FlowReservationResponses.Create(ctx, "e1", "R1", frp); err != nil {
		t.Fatal(err)
	}
	check := func() error {
		w := commitment.Window{Start: 1200, Duration: 10}
		return s.CommitmentLedger.Within(ctx, []string{lfdi}, func(v commitment.View) error {
			return v.CheckGrant(ctx, lfdi, &w, "")
		})
	}
	var ce *commitment.ConflictError
	if err := check(); !errors.As(err, &ce) || ce.MRID != "MRID-R1" {
		t.Fatalf("CheckGrant = %v, want a conflict naming MRID-R1", err)
	}
	at := int64(1100)
	if err := s.FlowReservationResponseLifecycles.Create(ctx, "e1", "R1", dercontrol.LifecycleRecord{CancelledAt: &at}); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatalf("after the cancel mark: CheckGrant = %v, want the window free", err)
	}
}
