package flowreservation

import (
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func requestWithWindow() sep2.FlowReservationRequest {
	return sep2.FlowReservationRequest{
		MRID:              "REQ1",
		EnergyRequested:   &sep2.SignedRealEnergy{Value: 10000}, // charging positive
		PowerRequested:    &sep2.ActivePower{Value: 5000},
		IntervalRequested: &sep2.DateTimeInterval{Start: 1000, Duration: 3600},
	}
}

// TestAnswerFor_GrantAsAsked is #666's second criterion, the "grant as
// asked" branch: the zero Decision (Kind Grant, every override nil) copies
// the request's own interval, energy and power unchanged.
func TestAnswerFor_GrantAsAsked(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow()

	frp, err := answerFor(frq, Decision{})
	if err != nil {
		t.Fatalf("answerFor: %v", err)
	}
	if frp.Interval == nil || *frp.Interval != *frq.IntervalRequested {
		t.Errorf("Interval = %+v, want the requested interval %+v", frp.Interval, frq.IntervalRequested)
	}
	if frp.EnergyAvailable == nil || *frp.EnergyAvailable != *frq.EnergyRequested {
		t.Errorf("EnergyAvailable = %+v, want the requested energy %+v", frp.EnergyAvailable, frq.EnergyRequested)
	}
	if frp.PowerAvailable == nil || *frp.PowerAvailable != *frq.PowerRequested {
		t.Errorf("PowerAvailable = %+v, want the requested power %+v", frp.PowerAvailable, frq.PowerRequested)
	}
}

// TestAnswerFor_GrantWithOperatorValues is the "grant with the operator's
// energy, power and interval inside the requested window" branch: a
// narrower interval and lower magnitudes, still charging positive, are
// accepted and stored exactly as set.
func TestAnswerFor_GrantWithOperatorValues(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow()
	decision := Decision{
		Kind:     Grant,
		Interval: &sep2.DateTimeInterval{Start: 1100, Duration: 1000},
		Energy:   &sep2.SignedRealEnergy{Value: 4000},
		Power:    &sep2.ActivePower{Value: 2000},
	}

	frp, err := answerFor(frq, decision)
	if err != nil {
		t.Fatalf("answerFor: %v", err)
	}
	if *frp.Interval != *decision.Interval {
		t.Errorf("Interval = %+v, want %+v", frp.Interval, decision.Interval)
	}
	if *frp.EnergyAvailable != *decision.Energy {
		t.Errorf("EnergyAvailable = %+v, want %+v", frp.EnergyAvailable, decision.Energy)
	}
	if *frp.PowerAvailable != *decision.Power {
		t.Errorf("PowerAvailable = %+v, want %+v", frp.PowerAvailable, decision.Power)
	}
}

// TestAnswerFor_Deny is #666's deny branch: interval duration zero,
// interval.start the requested start, energyAvailable and powerAvailable
// both zero.
func TestAnswerFor_Deny(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow()

	frp, err := answerFor(frq, Decision{Kind: Deny})
	if err != nil {
		t.Fatalf("answerFor: %v", err)
	}
	if frp.Interval == nil || frp.Interval.Duration != 0 {
		t.Fatalf("Interval = %+v, want duration 0", frp.Interval)
	}
	if frp.Interval.Start != frq.IntervalRequested.Start {
		t.Errorf("Interval.Start = %d, want the requested start %d", frp.Interval.Start, frq.IntervalRequested.Start)
	}
	if frp.EnergyAvailable == nil || frp.EnergyAvailable.Value != 0 {
		t.Errorf("EnergyAvailable = %+v, want 0", frp.EnergyAvailable)
	}
	if frp.PowerAvailable == nil || frp.PowerAvailable.Value != 0 {
		t.Errorf("PowerAvailable = %+v, want 0", frp.PowerAvailable)
	}
}

// TestAnswerFor_DenyWithNoRequestedInterval covers a request that named no
// window at all: the denial still carries a zero-duration interval, start
// 0, rather than a nil one (energyAvailable and powerAvailable are
// mandatory elements; so is interval, once EventStatus needs to derive
// from it).
func TestAnswerFor_DenyWithNoRequestedInterval(t *testing.T) {
	t.Parallel()
	frq := sep2.FlowReservationRequest{MRID: "REQ1"}

	frp, err := answerFor(frq, Decision{Kind: Deny})
	if err != nil {
		t.Fatalf("answerFor: %v", err)
	}
	if frp.Interval == nil || frp.Interval.Start != 0 || frp.Interval.Duration != 0 {
		t.Errorf("Interval = %+v, want {Start:0 Duration:0}", frp.Interval)
	}
}

// TestAnswerFor_IntervalBounds is #666's "a grant never exceeds or
// reverses the request" criterion, interval field: an operator interval
// that starts before the window, or ends after it, is refused; one that
// lies exactly on the boundary is accepted.
func TestAnswerFor_IntervalBounds(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow() // window [1000, 4600)

	for _, tc := range []struct {
		name    string
		want    sep2.DateTimeInterval
		wantErr error
	}{
		{"starts before window", sep2.DateTimeInterval{Start: 999, Duration: 100}, ErrIntervalOutsideWindow},
		{"ends after window", sep2.DateTimeInterval{Start: 4500, Duration: 200}, ErrIntervalOutsideWindow},
		{"exactly the window", sep2.DateTimeInterval{Start: 1000, Duration: 3600}, nil},
		{"strictly inside", sep2.DateTimeInterval{Start: 1500, Duration: 100}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := answerFor(frq, Decision{Kind: Grant, Interval: &tc.want})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestAnswerFor_IntervalWithNoRequestedWindow refuses an operator interval
// when the request named none: there is nothing to validate "inside" against.
func TestAnswerFor_IntervalWithNoRequestedWindow(t *testing.T) {
	t.Parallel()
	frq := sep2.FlowReservationRequest{MRID: "REQ1"}
	_, err := answerFor(frq, Decision{Kind: Grant, Interval: &sep2.DateTimeInterval{Start: 0, Duration: 10}})
	if !errors.Is(err, ErrNoRequestedWindow) {
		t.Errorf("err = %v, want ErrNoRequestedWindow", err)
	}
}

// TestAnswerFor_EnergyBounds is the energy field of the "never exceeds or
// reverses" criterion: a magnitude above the requested one is refused, a
// negative (discharging) value against a charging-positive request is
// refused as a reversal, and a smaller same-direction value is accepted.
func TestAnswerFor_EnergyBounds(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow() // EnergyRequested.Value = 10000 (charging positive)

	for _, tc := range []struct {
		name    string
		granted sep2.SignedRealEnergy
		wantErr error
	}{
		{"exceeds magnitude", sep2.SignedRealEnergy{Value: 10001}, ErrEnergyExceedsRequest},
		{"reverses sign", sep2.SignedRealEnergy{Value: -1}, ErrEnergyReversesRequest},
		{"exact magnitude, same sign", sep2.SignedRealEnergy{Value: 10000}, nil},
		{"lower magnitude, same sign", sep2.SignedRealEnergy{Value: 4000}, nil},
		{"zero is never a reversal", sep2.SignedRealEnergy{Value: 0}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := answerFor(frq, Decision{Kind: Grant, Energy: &tc.granted})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestAnswerFor_EnergyBounds_Multiplier proves the magnitude comparison
// applies the multiplier rather than comparing raw Value: 500 at
// multiplier 1 (5000 Wh applied) is inside a 10000 Wh request even though
// the raw Value alone would read as smaller than it should, and 2000 at
// multiplier 1 (20000 Wh applied) exceeds it even though the raw Value
// alone reads as smaller than the requested 10000.
func TestAnswerFor_EnergyBounds_Multiplier(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow() // EnergyRequested = {Value: 10000, Multiplier: 0} = 10000 Wh

	if _, err := answerFor(frq, Decision{Kind: Grant, Energy: &sep2.SignedRealEnergy{Value: 500, Multiplier: 1}}); err != nil {
		t.Errorf("500e1 = 5000 Wh, inside 10000 Wh: err = %v, want nil", err)
	}
	if _, err := answerFor(frq, Decision{Kind: Grant, Energy: &sep2.SignedRealEnergy{Value: 2000, Multiplier: 1}}); !errors.Is(err, ErrEnergyExceedsRequest) {
		t.Errorf("2000e1 = 20000 Wh, exceeds 10000 Wh: err = %v, want ErrEnergyExceedsRequest", err)
	}
}

// TestAnswerFor_EnergyWithNoRequestedEnergy refuses an operator energy
// value when the request named none.
func TestAnswerFor_EnergyWithNoRequestedEnergy(t *testing.T) {
	t.Parallel()
	frq := sep2.FlowReservationRequest{MRID: "REQ1"}
	_, err := answerFor(frq, Decision{Kind: Grant, Energy: &sep2.SignedRealEnergy{Value: 1}})
	if !errors.Is(err, ErrNoRequestedEnergy) {
		t.Errorf("err = %v, want ErrNoRequestedEnergy", err)
	}
}

// TestAnswerFor_PowerBounds is the power field of the "never exceeds or
// reverses" criterion, mirroring TestAnswerFor_EnergyBounds.
func TestAnswerFor_PowerBounds(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow() // PowerRequested.Value = 5000

	for _, tc := range []struct {
		name    string
		granted sep2.ActivePower
		wantErr error
	}{
		{"exceeds magnitude", sep2.ActivePower{Value: 5001}, ErrPowerExceedsRequest},
		{"reverses sign", sep2.ActivePower{Value: -1}, ErrPowerReversesRequest},
		{"exact magnitude, same sign", sep2.ActivePower{Value: 5000}, nil},
		{"lower magnitude, same sign", sep2.ActivePower{Value: 2000}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := answerFor(frq, Decision{Kind: Grant, Power: &tc.granted})
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestAnswerFor_PowerWithNoRequestedPower refuses an operator power value
// when the request named none.
func TestAnswerFor_PowerWithNoRequestedPower(t *testing.T) {
	t.Parallel()
	frq := sep2.FlowReservationRequest{MRID: "REQ1"}
	_, err := answerFor(frq, Decision{Kind: Grant, Power: &sep2.ActivePower{Value: 1}})
	if !errors.Is(err, ErrNoRequestedPower) {
		t.Errorf("err = %v, want ErrNoRequestedPower", err)
	}
}

// TestDeriveEventStatus is #666's fifth criterion as applied by this
// package's standalone status rule (see status.go): Scheduled before the
// start, Active inside the interval, Complete once duration has elapsed.
func TestDeriveEventStatus(t *testing.T) {
	t.Parallel()
	interval := sep2.DateTimeInterval{Start: 1000, Duration: 100}

	for _, tc := range []struct {
		name string
		now  int64
		want uint8
	}{
		{"before start", 999, sep2.EventStatusScheduled},
		{"at start", 1000, sep2.EventStatusActive},
		{"inside", 1050, sep2.EventStatusActive},
		{"at end (duration elapsed)", 1100, sep2.EventStatusComplete},
		{"well after end", 5000, sep2.EventStatusComplete},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := deriveEventStatus(interval, tc.now)
			if got.CurrentStatus != tc.want {
				t.Errorf("CurrentStatus = %d, want %d", got.CurrentStatus, tc.want)
			}
		})
	}
}

// TestDeriveEventStatus_ZeroDurationNeverActive is the denial edge the
// design flags: a zero-duration interval's active span is empty, so the
// derivation must report Scheduled or Complete for it, never Active, at
// any "now".
func TestDeriveEventStatus_ZeroDurationNeverActive(t *testing.T) {
	t.Parallel()
	interval := sep2.DateTimeInterval{Start: 1000, Duration: 0}

	for _, now := range []int64{0, 999, 1000, 1001, 5000} {
		got := deriveEventStatus(interval, now)
		if got.CurrentStatus == sep2.EventStatusActive {
			t.Errorf("now=%d: CurrentStatus = Active, want Scheduled or Complete for a zero-duration interval", now)
		}
	}
}
