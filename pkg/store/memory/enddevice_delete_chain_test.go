package memory_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// The EndDevice DELETE chain crosses decorator boundaries: assembly.go
// composes FlowReservationLinkedEndDeviceStore around
// LogEventLinkedEndDeviceStore around the registration-bound store. This
// file pins the cross-decorator half of the GRIDAPPSD/ieee-2030_5-server-go#701
// fix-round-1 finding: a failure in the INNER decorator's own cascade must
// leave the OUTER decorator's records untouched too, not just its own.

// TestFlowReservationOverLogEvent_DeleteLeavesFlowReservationRecordsUntouchedWhenLogEventCascadeFails
// reproduces the reported shape directly: "the outer flow decorator empties
// frq and frp before logeventbinding.go's cascade fails". Requests and
// responses cascade cleanly on their own; only the inner LogEvent cascade is
// made to fail, and that failure must still stop the outer decorator's own
// records from being removed.
func TestFlowReservationOverLogEvent_DeleteLeavesFlowReservationRecordsUntouchedWhenLogEventCascadeFails(t *testing.T) {
	t.Parallel()

	reqs := memory.NewScopedStore[sep2.FlowReservationRequest]()
	resps := memory.NewScopedStore[sep2.FlowReservationResponse]()
	fault := &storetest.Fault{}
	events := storetest.NewFaultyScopedStore[sep2.LogEvent](memory.NewScopedStore[sep2.LogEvent](), fault)

	inner := memory.NewEndDeviceStore()
	logStore := memory.NewLogEventLinkedEndDeviceStore(inner, events)
	s := memory.NewFlowReservationLinkedEndDeviceStore(logStore, reqs, resps)
	ctx := context.Background()

	dev := sep2.EndDevice{SFDI: "1111111111", LFDI: "AAAA"}
	dev.Href = "/edev/1"
	if err := s.Create(ctx, "1", dev); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := reqs.Create(ctx, "1", "req-1", sep2.FlowReservationRequest{MRID: "req-1"}); err != nil {
		t.Fatalf("seed request: %v", err)
	}
	if err := resps.Create(ctx, "1", "resp-1", sep2.FlowReservationResponse{Subject: "req-1"}); err != nil {
		t.Fatalf("seed response: %v", err)
	}

	// Control: both records exist and events is not yet armed, so a delete
	// right now would succeed; the fault below is what must make the whole
	// chain's delete fail.
	if n, err := reqs.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: requests under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if n, err := resps.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: responses under %q = %d, %v, want 1, nil", "1", n, err)
	}

	fault.Arm(storetest.ErrBackendUnavailable)
	if err := s.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded while the inner LogEvent store could not cascade; want an error and everything left in place")
	}

	if n, err := reqs.Count(ctx, "1"); err != nil || n != 1 {
		t.Errorf("requests under %q = %d, %v, want 1, nil: the outer decorator's own cascade must not run "+
			"ahead of a failure one layer down", "1", n, err)
	}
	if n, err := resps.Count(ctx, "1"); err != nil || n != 1 {
		t.Errorf("responses under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if _, err := inner.Get(ctx, "1"); err != nil {
		t.Errorf("device was removed despite the failed cascade: Get(%q) = %v, want the device still present", "1", err)
	}
}
