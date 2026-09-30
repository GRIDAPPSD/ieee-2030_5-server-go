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
// file pins the cross-decorator findings on GRIDAPPSD/ieee-2030_5-server-go#701:
// a failure anywhere in the chain that the probe can see must be refused
// before any layer, inner or outer, removes anything.

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

// TestFullChain_DeleteLeavesEveryLayerUntouchedWhenRegistrationsCascadeFails
// is the full-chain reproduction of the fix-round-2 finding: before
// RegisteredEndDeviceStore joined the probe chain, it sat beneath both
// probed decorators unprobed, so a Registration store that could not delete
// was discovered only after the flow reservation and LogEvent cascades had
// already removed their own records and the device itself. Every layer in
// the real assembly.go composition is exercised here: flow reservation over
// LogEvent over the registration binding.
func TestFullChain_DeleteLeavesEveryLayerUntouchedWhenRegistrationsCascadeFails(t *testing.T) {
	t.Parallel()

	reqs := memory.NewScopedStore[sep2.FlowReservationRequest]()
	resps := memory.NewScopedStore[sep2.FlowReservationResponse]()
	events := memory.NewScopedStore[sep2.LogEvent]()

	regsInner := memory.NewRegistrationStore()
	fault := &storetest.Fault{}
	regs := storetest.NewFaultyResourceStore[sep2.Registration](regsInner, fault)

	rawDevs := memory.NewEndDeviceStore()
	registered := memory.NewRegisteredEndDeviceStore(rawDevs, regs, memory.RegistrationPolicy{
		PIN: func(string) (uint32, bool) { return 123455, true },
	})
	logStore := memory.NewLogEventLinkedEndDeviceStore(registered, events)
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
	if err := events.Create(ctx, "1", "evt-1", sep2.LogEvent{LogEventID: 1}); err != nil {
		t.Fatalf("seed log event: %v", err)
	}

	// Control: every record exists, the registration was provisioned by
	// Create above, and regs is not yet armed, so a delete right now would
	// succeed; the fault below is what must make the whole chain's delete
	// fail.
	if n, err := reqs.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: requests under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if n, err := resps.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: responses under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if n, err := events.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: log events under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if _, err := regsInner.Get(ctx, "1"); err != nil {
		t.Fatalf("control: registration under %q: %v, want present", "1", err)
	}

	fault.Arm(storetest.ErrBackendUnavailable)
	if err := s.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded while the Registration store could not cascade; want an error and every layer left in place")
	}

	if n, err := reqs.Count(ctx, "1"); err != nil || n != 1 {
		t.Errorf("requests under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if n, err := resps.Count(ctx, "1"); err != nil || n != 1 {
		t.Errorf("responses under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if n, err := events.Count(ctx, "1"); err != nil || n != 1 {
		t.Errorf("log events under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if _, err := rawDevs.Get(ctx, "1"); err != nil {
		t.Errorf("device was removed despite the failed probe: Get(%q) = %v, want the device still present", "1", err)
	}
	fault.Disarm()
	if _, err := regsInner.Get(ctx, "1"); err != nil {
		t.Errorf("registration was removed despite the failed probe: Get(%q) = %v, want the registration still present", "1", err)
	}
}

// TestFlowReservationOverLogEvent_DeleteLeavesRequestsAndResponsesUntouchedWhenLogEventsCannotCascade
// uses a LogEvents store that answers HasParent normally but implements no
// DeleteParent at all (storetest.ScopedFake, a second, independent
// store.ScopedStore implementation, not the in-memory one deleteScopedParent
// and probeScopedParent are usually exercised against). It kills the mutant
// that disables probeScopedParent's own capability check and leaves only its
// HasParent call: that mutant does not affect an armed-fault store, because
// HasParent already fails on one, but it does let a probe wrongly pass on a
// store whose HasParent works and whose DeleteParent capability is simply
// absent, which is exactly this shape.
func TestFlowReservationOverLogEvent_DeleteLeavesRequestsAndResponsesUntouchedWhenLogEventsCannotCascade(t *testing.T) {
	t.Parallel()

	reqs := memory.NewScopedStore[sep2.FlowReservationRequest]()
	resps := memory.NewScopedStore[sep2.FlowReservationResponse]()
	events := storetest.NewScopedFake[sep2.LogEvent]()

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
	if err := events.Create(ctx, "1", "evt-1", sep2.LogEvent{LogEventID: 1}); err != nil {
		t.Fatalf("seed log event: %v", err)
	}

	// Control: every record exists and events answers HasParent normally
	// (ScopedFake has no fault switch to arm), so what must refuse the
	// delete is the missing DeleteParent capability alone.
	if n, err := reqs.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: requests under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if n, err := resps.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: responses under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if n, err := events.Count(ctx, "1"); err != nil || n != 1 {
		t.Fatalf("control: log events under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if has, err := events.HasParent(ctx, "1"); err != nil || !has {
		t.Fatalf("control: HasParent(%q) on events = %v, %v, want true, nil: a fake whose own read fails proves nothing about the capability check", "1", has, err)
	}

	if err := s.Delete(ctx, "1"); err == nil {
		t.Fatal("Delete succeeded over a LogEvents store with no DeleteParent capability; want an error and everything left in place")
	}

	if n, err := reqs.Count(ctx, "1"); err != nil || n != 1 {
		t.Errorf("requests under %q = %d, %v, want 1, nil: probeScopedParent's capability check must refuse "+
			"before the outer decorator's own cascade runs", "1", n, err)
	}
	if n, err := resps.Count(ctx, "1"); err != nil || n != 1 {
		t.Errorf("responses under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if n, err := events.Count(ctx, "1"); err != nil || n != 1 {
		t.Errorf("log events under %q = %d, %v, want 1, nil", "1", n, err)
	}
	if _, err := inner.Get(ctx, "1"); err != nil {
		t.Errorf("device was removed despite the failed probe: Get(%q) = %v, want the device still present", "1", err)
	}
}
