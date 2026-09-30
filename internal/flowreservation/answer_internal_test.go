package flowreservation

import (
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
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

// TestAnswerFor_GrantRefusedOnCancelledRequest is #736's error-handling
// LOW: 10.9.3.1 makes RequestStatus 1 (Cancelled) a withdrawal, so a Grant
// on a cancelled request is refused, whether it comes from the deadline
// fallback (which never even tries, see Queue.attemptFallback) or an
// operator's explicit Answer once #670 exists.
func TestAnswerFor_GrantRefusedOnCancelledRequest(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow()
	frq.RequestStatus.RequestStatus = sep2.RequestStatusCancelled

	_, err := answerFor(frq, Decision{})
	if !errors.Is(err, ErrRequestCancelled) {
		t.Errorf("err = %v, want ErrRequestCancelled", err)
	}
}

// TestAnswerFor_DenyAllowedOnCancelledRequest: denying a cancelled request
// is not a Grant, so it is not refused by the cancellation guard.
func TestAnswerFor_DenyAllowedOnCancelledRequest(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow()
	frq.RequestStatus.RequestStatus = sep2.RequestStatusCancelled

	_, err := answerFor(frq, Decision{Kind: Deny})
	if err != nil {
		t.Errorf("answerFor(Deny) on a cancelled request: %v, want nil", err)
	}
}

// TestAnswerFor_GrantZeroDurationRefused is #736's small item: 10.9.3.2
// reserves interval duration zero for a denial, so a Grant can never
// produce it, whether the zero comes from an operator override or from
// echoing a request that asked for a zero-length window.
func TestAnswerFor_GrantZeroDurationRefused(t *testing.T) {
	t.Parallel()

	t.Run("operator override", func(t *testing.T) {
		t.Parallel()
		frq := requestWithWindow()
		decision := Decision{Kind: Grant, Interval: &sep2.DateTimeInterval{Start: 1000, Duration: 0}}
		_, err := answerFor(frq, decision)
		if !errors.Is(err, ErrGrantZeroDuration) {
			t.Errorf("err = %v, want ErrGrantZeroDuration", err)
		}
	})

	t.Run("grant as asked, request itself asked for zero duration", func(t *testing.T) {
		t.Parallel()
		frq := requestWithWindow()
		frq.IntervalRequested = &sep2.DateTimeInterval{Start: 1000, Duration: 0}
		_, err := answerFor(frq, Decision{})
		if !errors.Is(err, ErrGrantZeroDuration) {
			t.Errorf("err = %v, want ErrGrantZeroDuration", err)
		}
	})
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
// window at all: the denial still carries a zero-duration interval, rather
// than a nil one (energyAvailable and powerAvailable are mandatory
// elements; so is interval, once EventStatus needs to derive from it).
// start falls back to the request's own creationTime, not the zero value:
// 0 is a real wire timestamp (1970-01-01), and nobody supplied it.
func TestAnswerFor_DenyWithNoRequestedInterval(t *testing.T) {
	t.Parallel()
	frq := sep2.FlowReservationRequest{MRID: "REQ1", CreationTime: 123456}

	frp, err := answerFor(frq, Decision{Kind: Deny})
	if err != nil {
		t.Fatalf("answerFor: %v", err)
	}
	if frp.Interval == nil || frp.Interval.Start != frq.CreationTime || frp.Interval.Duration != 0 {
		t.Errorf("Interval = %+v, want {Start:%d Duration:0}", frp.Interval, frq.CreationTime)
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
		// The window ends at 4600 (Start 1000 + Duration 3600). These two
		// pin that exact boundary: one second past it is refused, and a
		// mutant loosening the end check to outerEnd+1 only shows up here,
		// not against "ends after window" above, which ends 100s past.
		{"ends exactly at the window end", sep2.DateTimeInterval{Start: 4500, Duration: 100}, nil},
		{"ends one second past the window end", sep2.DateTimeInterval{Start: 4500, Duration: 101}, ErrIntervalOutsideWindow},
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

// TestAnswerFor_EnergyBounds_Discharge is TestAnswerFor_EnergyBounds's
// mirror in the discharging (negative) direction: every bound test above
// uses a positive request, so a mutant dropping the Abs in the magnitude
// compare, or one that refuses any negative grant outright rather than
// only a reversal, both pass every test above and only fail here.
func TestAnswerFor_EnergyBounds_Discharge(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow()
	frq.EnergyRequested = &sep2.SignedRealEnergy{Value: -10000} // discharging

	for _, tc := range []struct {
		name    string
		granted sep2.SignedRealEnergy
		wantErr error
	}{
		{"exceeds magnitude", sep2.SignedRealEnergy{Value: -10001}, ErrEnergyExceedsRequest},
		{"reverses sign (positive against a discharging request)", sep2.SignedRealEnergy{Value: 1}, ErrEnergyReversesRequest},
		{"exact magnitude, same sign", sep2.SignedRealEnergy{Value: -10000}, nil},
		{"lower magnitude, same sign", sep2.SignedRealEnergy{Value: -4000}, nil},
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

// TestAnswerFor_EnergyBounds_ExactMagnitude is #736's security LOW: the
// magnitude compare is exact integer arithmetic, not float64, which loses
// precision past 2^53 and would read 2^53+1 as equal to 2^53 rather than
// exceeding it.
func TestAnswerFor_EnergyBounds_ExactMagnitude(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow()
	const twoPow53 = int64(1) << 53
	frq.EnergyRequested = &sep2.SignedRealEnergy{Value: twoPow53}

	if _, err := answerFor(frq, Decision{Kind: Grant, Energy: &sep2.SignedRealEnergy{Value: twoPow53}}); err != nil {
		t.Errorf("granting exactly the requested 2^53: err = %v, want nil", err)
	}
	if _, err := answerFor(frq, Decision{Kind: Grant, Energy: &sep2.SignedRealEnergy{Value: twoPow53 + 1}}); !errors.Is(err, ErrEnergyExceedsRequest) {
		t.Errorf("granting 2^53+1 against a 2^53 request: err = %v, want ErrEnergyExceedsRequest", err)
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

// TestAnswerFor_EnergyBounds_RequestMultiplierAboveGrant is
// TestAnswerFor_EnergyBounds_Multiplier's mirror: there the GRANT carried
// the larger multiplier, exercising magnitudeExceeds's aMul > bMul branch;
// here the REQUEST does, exercising the bMul > aMul branch, which the
// other test's shape cannot reach.
func TestAnswerFor_EnergyBounds_RequestMultiplierAboveGrant(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow()
	frq.EnergyRequested = &sep2.SignedRealEnergy{Value: 100, Multiplier: 2} // 10000 Wh applied

	if _, err := answerFor(frq, Decision{Kind: Grant, Energy: &sep2.SignedRealEnergy{Value: 9999, Multiplier: 0}}); err != nil {
		t.Errorf("9999 Wh, inside 10000 Wh (100e2): err = %v, want nil", err)
	}
	if _, err := answerFor(frq, Decision{Kind: Grant, Energy: &sep2.SignedRealEnergy{Value: 10001, Multiplier: 0}}); !errors.Is(err, ErrEnergyExceedsRequest) {
		t.Errorf("10001 Wh, exceeds 10000 Wh (100e2): err = %v, want ErrEnergyExceedsRequest", err)
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

// TestAnswerFor_PowerBounds_Discharge mirrors
// TestAnswerFor_EnergyBounds_Discharge for power.
func TestAnswerFor_PowerBounds_Discharge(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow()
	frq.PowerRequested = &sep2.ActivePower{Value: -5000} // discharging

	for _, tc := range []struct {
		name    string
		granted sep2.ActivePower
		wantErr error
	}{
		{"exceeds magnitude", sep2.ActivePower{Value: -5001}, ErrPowerExceedsRequest},
		{"reverses sign (positive against a discharging request)", sep2.ActivePower{Value: 1}, ErrPowerReversesRequest},
		{"exact magnitude, same sign", sep2.ActivePower{Value: -5000}, nil},
		{"lower magnitude, same sign", sep2.ActivePower{Value: -2000}, nil},
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

// TestAnswerFor_PowerBounds_ZeroGrant is coverage's LOW: a Value-0 power
// override (asking for no power at all, e.g. to pair with a zero-power but
// non-zero-energy grant) is a real value, not the "no override" nil case,
// and must never be read as a reversal.
func TestAnswerFor_PowerBounds_ZeroGrant(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow()
	_, err := answerFor(frq, Decision{Kind: Grant, Power: &sep2.ActivePower{Value: 0}})
	if err != nil {
		t.Errorf("granting zero power: err = %v, want nil", err)
	}
}

// TestAnswerFor_PowerBounds_Multiplier mirrors
// TestAnswerFor_EnergyBounds_Multiplier for power.
func TestAnswerFor_PowerBounds_Multiplier(t *testing.T) {
	t.Parallel()
	frq := requestWithWindow() // PowerRequested = {Value: 5000, Multiplier: 0} = 5000 W

	if _, err := answerFor(frq, Decision{Kind: Grant, Power: &sep2.ActivePower{Value: 400, Multiplier: 1}}); err != nil {
		t.Errorf("400e1 = 4000 W, inside 5000 W: err = %v, want nil", err)
	}
	if _, err := answerFor(frq, Decision{Kind: Grant, Power: &sep2.ActivePower{Value: 600, Multiplier: 1}}); !errors.Is(err, ErrPowerExceedsRequest) {
		t.Errorf("600e1 = 6000 W, exceeds 5000 W: err = %v, want ErrPowerExceedsRequest", err)
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

// TestDeriveEventStatus is #666's fifth criterion, reusing #564's
// dercontrol.DeriveStatus (see status.go): Scheduled before the effective
// start (the later of start and creationTime), Active from then on, and
// never Complete, however long past start now is, since DeriveStatus
// takes no duration and #564 deliberately never returns that 2023-only
// value.
func TestDeriveEventStatus(t *testing.T) {
	t.Parallel()
	const start, creationTime = int64(1000), int64(1000)

	for _, tc := range []struct {
		name string
		now  int64
		want uint8
	}{
		{"before start", 999, sep2.EventStatusScheduled},
		{"at start", 1000, sep2.EventStatusActive},
		{"well after start", 5000, sep2.EventStatusActive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := deriveEventStatus(start, creationTime, tc.now, dercontrol.LifecycleRecord{})
			if got.CurrentStatus != tc.want {
				t.Errorf("CurrentStatus = %d, want %d", got.CurrentStatus, tc.want)
			}
		})
	}
}

// TestDeriveEventStatus_ZeroDurationReadsActiveNeverComplete is the denial
// edge the design flags: this package no longer takes duration at all, so
// a zero-duration denial reads by the same start-versus-now rule as any
// grant, Active once its start has passed, never Complete, the same
// property #564 already gives an ended DERControl.
func TestDeriveEventStatus_ZeroDurationReadsActiveNeverComplete(t *testing.T) {
	t.Parallel()
	const start, creationTime = int64(1000), int64(1000)

	for _, now := range []int64{1000, 1001, 5000} {
		got := deriveEventStatus(start, creationTime, now, dercontrol.LifecycleRecord{})
		if got.CurrentStatus != sep2.EventStatusActive {
			t.Errorf("now=%d: CurrentStatus = %d, want Active (never Complete)", now, got.CurrentStatus)
		}
	}
}

// TestDeriveEventStatus_CreationTimeBumpsEffectiveStart mirrors
// dercontrol's own TestDeriveStatusEffectiveStartIsLaterOfStartAndCreationTime:
// a creationTime later than start delays the Scheduled-to-Active
// transition to creationTime, not start, since a response must not read
// Active before it was even created.
func TestDeriveEventStatus_CreationTimeBumpsEffectiveStart(t *testing.T) {
	t.Parallel()
	const start, creationTime = int64(1000), int64(2000)

	if got := deriveEventStatus(start, creationTime, 1500, dercontrol.LifecycleRecord{}); got.CurrentStatus != sep2.EventStatusScheduled {
		t.Errorf("now=1500 (past start, before the bumped creationTime): CurrentStatus = %d, want Scheduled", got.CurrentStatus)
	}
	if got := deriveEventStatus(start, creationTime, 2000, dercontrol.LifecycleRecord{}); got.CurrentStatus != sep2.EventStatusActive {
		t.Errorf("now=2000 (at the bumped creationTime): CurrentStatus = %d, want Active", got.CurrentStatus)
	}
}
