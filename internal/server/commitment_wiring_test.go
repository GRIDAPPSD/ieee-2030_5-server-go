package server_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// TestNewCommitmentLedger_ReadsTheStoresCancelMark: the ledger Run builds
// reads the same response lifecycle store the response routes read, so a
// cancel mark written there frees the grant's window.
func TestNewCommitmentLedger_ReadsTheStoresCancelMark(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newTestStores()
	s.CommitmentLedger = server.NewCommitmentLedger(s)
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
		t.Fatalf("before the cancel mark: CheckGrant = %v, want a conflict naming MRID-R1", err)
	}
	at := int64(1100)
	if err := s.FlowReservationResponseLifecycles.Create(ctx, "e1", "R1", dercontrol.LifecycleRecord{CancelledAt: &at}); err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatalf("after the cancel mark: CheckGrant = %v, want the window free", err)
	}
}
