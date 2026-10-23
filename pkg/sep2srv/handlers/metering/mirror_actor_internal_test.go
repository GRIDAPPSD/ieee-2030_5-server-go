package metering

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestNewMirrorActor_DecisionAndReasonClass pins the decision the actor
// returns for a manager with and without the device record, and the denial
// reason class each maps to: the unregistered class must not fall back to
// the generic one, since it is how an operator tells a missing record from a
// missing pair.
func TestNewMirrorActor_DecisionAndReasonClass(t *testing.T) {
	const device = "1111111111111111111111111111111111111111"
	const manager = "2222222222222222222222222222222222222222"

	pairs := memory.NewEndDeviceManagementStore()
	if err := pairs.Assign(context.Background(), manager, device); err != nil {
		t.Fatalf("assign pair: %v", err)
	}
	devices := memory.NewEndDeviceStore()
	actFor := newMirrorActor(pairs, RequireDeviceRecords(devices))

	got, err := actFor(context.Background(), manager, device)
	if err != nil || got != mirrorUnregistered {
		t.Fatalf("manager, no record: decision = %v, err = %v, want mirrorUnregistered", got, err)
	}
	if r := got.denialReason("fallback"); r != "managed-device-unregistered" {
		t.Errorf("reason = %q, want managed-device-unregistered", r)
	}

	if got, err := actFor(context.Background(), "3333333333333333333333333333333333333333", device); err != nil || got != mirrorRefused {
		t.Errorf("unpaired caller: decision = %v, err = %v, want mirrorRefused", got, err)
	} else if r := got.denialReason("fallback"); r != "fallback" {
		t.Errorf("reason = %q, want the caller's own class", r)
	}

	if err := devices.Create(context.Background(), "1", sep2.EndDevice{LFDI: device}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	if got, err := actFor(context.Background(), manager, device); err != nil || got != mirrorAllowed {
		t.Errorf("manager, record present: decision = %v, err = %v, want mirrorAllowed", got, err)
	}
}
