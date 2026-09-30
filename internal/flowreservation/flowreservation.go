// Package flowreservation is the answer path for IEEE 2030.5 flow
// reservation requests: it holds a request until the operator answers or a
// deadline passes, then builds the one FlowReservationResponse that answers
// it (#666).
//
// A request creates no response by itself (POST /edev/{id}/frq only stores
// it and calls Queue.Submit). Every response, whether the operator's answer
// or the deadline fallback, is built by Queue.build, so there is exactly one
// code path that ever constructs a FlowReservationResponse.
package flowreservation

import (
	"errors"
	"math"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// Errors a Decision can fail with. ErrAlreadyAnswered is also the outcome
// when the deadline fallback loses a race with the operator's own answer
// (or the reverse): whichever call reaches Queue.build first wins, and the
// other gets this back rather than building a second response.
var (
	ErrAlreadyAnswered       = errors.New("flowreservation: request already answered")
	ErrNoRequestedWindow     = errors.New("flowreservation: operator set an interval but the request named none")
	ErrIntervalOutsideWindow = errors.New("flowreservation: granted interval is outside the requested window")
	ErrNoRequestedEnergy     = errors.New("flowreservation: operator set energy but the request named none")
	ErrEnergyExceedsRequest  = errors.New("flowreservation: granted energy exceeds the requested magnitude")
	ErrEnergyReversesRequest = errors.New("flowreservation: granted energy reverses the requested direction")
	ErrNoRequestedPower      = errors.New("flowreservation: operator set power but the request named none")
	ErrPowerExceedsRequest   = errors.New("flowreservation: granted power exceeds the requested magnitude")
	ErrPowerReversesRequest  = errors.New("flowreservation: granted power reverses the requested direction")
)

// DecisionKind selects how a Decision answers a pending request.
type DecisionKind int

const (
	// Grant answers in whole or in part; Decision's Interval, Energy and
	// Power name the granted values, or nil for "as asked".
	Grant DecisionKind = iota
	// Deny answers with interval duration zero, energyAvailable and
	// powerAvailable zero (10.9.3.2). Decision's other fields are ignored.
	Deny
)

// Decision is what the operator, or the deadline fallback, answers a
// pending FlowReservationRequest with. The zero value (Kind Grant, every
// override nil) grants exactly what was requested.
type Decision struct {
	Kind DecisionKind

	// Interval overrides the requested interval. It must lie inside
	// IntervalRequested: 10.9.1, a server "may create superseding events to
	// modify the interval within the requested timeframe." Nil grants the
	// requested interval unchanged.
	Interval *sep2.DateTimeInterval

	// Energy overrides EnergyRequested's magnitude, in the request's own
	// direction (our declared convention: charging positive, matching
	// energyRequested; 2023 states no sign for energyAvailable). Nil
	// grants the full requested amount.
	Energy *sep2.SignedRealEnergy

	// Power overrides PowerRequested's magnitude, same direction rule as
	// Energy. Nil grants the full requested amount.
	Power *sep2.ActivePower
}

// answerFor builds the FlowReservationResponse fields decision implies for
// frq, or an error if decision is not a valid answer to it (exceeds,
// reverses, or names a window/quantity the request never stated). It does
// not set mRID, Subject, CreationTime or EventStatus: those are Queue.build's
// job, since they depend on when and by which path the answer was built.
func answerFor(frq sep2.FlowReservationRequest, decision Decision) (sep2.FlowReservationResponse, error) {
	var frp sep2.FlowReservationResponse

	if decision.Kind == Deny {
		// 10.9.3.2: "If a server wants to deny a request, it SHALL create a
		// FlowReservationResponse with duration equal to zero." interval.start
		// is the requested start (Noor's design, Q1); energyAvailable and
		// powerAvailable are both mandatory elements and the standard fixes
		// neither value on a denial, so our declared convention zeroes both.
		var start int64
		if frq.IntervalRequested != nil {
			start = frq.IntervalRequested.Start
		}
		frp.Interval = &sep2.DateTimeInterval{Start: start, Duration: 0}
		frp.EnergyAvailable = &sep2.SignedRealEnergy{Value: 0}
		frp.PowerAvailable = &sep2.ActivePower{Value: 0}
		return frp, nil
	}

	interval, err := grantedInterval(frq, decision.Interval)
	if err != nil {
		return sep2.FlowReservationResponse{}, err
	}
	frp.Interval = interval

	energy, err := grantedEnergy(frq, decision.Energy)
	if err != nil {
		return sep2.FlowReservationResponse{}, err
	}
	frp.EnergyAvailable = energy

	power, err := grantedPower(frq, decision.Power)
	if err != nil {
		return sep2.FlowReservationResponse{}, err
	}
	frp.PowerAvailable = power

	return frp, nil
}

func grantedInterval(frq sep2.FlowReservationRequest, want *sep2.DateTimeInterval) (*sep2.DateTimeInterval, error) {
	if want == nil {
		if frq.IntervalRequested == nil {
			return nil, nil
		}
		cp := *frq.IntervalRequested
		return &cp, nil
	}
	if frq.IntervalRequested == nil {
		return nil, ErrNoRequestedWindow
	}
	if !intervalWithin(*want, *frq.IntervalRequested) {
		return nil, ErrIntervalOutsideWindow
	}
	cp := *want
	return &cp, nil
}

// intervalWithin reports whether inner lies inside outer: 10.9.1, a server
// "may create superseding events to modify the interval within the
// requested timeframe."
func intervalWithin(inner, outer sep2.DateTimeInterval) bool {
	innerEnd := inner.Start + int64(inner.Duration)
	outerEnd := outer.Start + int64(outer.Duration)
	return inner.Start >= outer.Start && innerEnd <= outerEnd
}

func grantedEnergy(frq sep2.FlowReservationRequest, want *sep2.SignedRealEnergy) (*sep2.SignedRealEnergy, error) {
	if want == nil {
		if frq.EnergyRequested == nil {
			return nil, nil
		}
		cp := *frq.EnergyRequested
		return &cp, nil
	}
	if frq.EnergyRequested == nil {
		return nil, ErrNoRequestedEnergy
	}
	granted := appliedValue(want.Value, want.Multiplier)
	requested := appliedValue(frq.EnergyRequested.Value, frq.EnergyRequested.Multiplier)
	if sign(granted) != 0 && sign(granted) != sign(requested) {
		return nil, ErrEnergyReversesRequest
	}
	if math.Abs(granted) > math.Abs(requested) {
		return nil, ErrEnergyExceedsRequest
	}
	cp := *want
	return &cp, nil
}

func grantedPower(frq sep2.FlowReservationRequest, want *sep2.ActivePower) (*sep2.ActivePower, error) {
	if want == nil {
		if frq.PowerRequested == nil {
			return nil, nil
		}
		cp := *frq.PowerRequested
		return &cp, nil
	}
	if frq.PowerRequested == nil {
		return nil, ErrNoRequestedPower
	}
	granted := appliedValue(int64(want.Value), want.Multiplier)
	requested := appliedValue(int64(frq.PowerRequested.Value), frq.PowerRequested.Multiplier)
	if sign(granted) != 0 && sign(granted) != sign(requested) {
		return nil, ErrPowerReversesRequest
	}
	if math.Abs(granted) > math.Abs(requested) {
		return nil, ErrPowerExceedsRequest
	}
	cp := *want
	return &cp, nil
}

// appliedValue applies a sep2 Multiplier (a power-of-ten exponent) to value,
// for magnitude and sign comparisons only; it is never used to build a wire
// value, so float64's precision loss is immaterial at grid-relevant
// magnitudes.
func appliedValue(value int64, multiplier int8) float64 {
	return float64(value) * math.Pow(10, float64(multiplier))
}

func sign(v float64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}
