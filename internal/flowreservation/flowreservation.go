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
	"math/big"

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
	ErrRequestCancelled      = errors.New("flowreservation: cannot grant a cancelled request")
	ErrGrantZeroDuration     = errors.New("flowreservation: a grant cannot carry interval duration zero, which 10.9.3.2 reserves for a denial")
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
		// is the requested start when there is one; a request that named no
		// window at all has no start to echo, so this falls back to the
		// request's own creationTime rather than the zero value, which would
		// be a wire timestamp nobody supplied. energyAvailable and
		// powerAvailable are both mandatory elements and the standard fixes
		// neither value on a denial, so our declared convention zeroes both.
		start := frq.CreationTime
		if frq.IntervalRequested != nil {
			start = frq.IntervalRequested.Start
		}
		frp.Interval = &sep2.DateTimeInterval{Start: start, Duration: 0}
		frp.EnergyAvailable = &sep2.SignedRealEnergy{Value: 0}
		frp.PowerAvailable = &sep2.ActivePower{Value: 0}
		return frp, nil
	}

	// 10.9.3.1: a Cancelled request (RequestStatus 1) is one the client
	// withdrew; granting it would answer a request nobody is waiting on
	// any more. Queue's deadline fallback already denies a cancelled
	// request without reaching here (there is no point trying a grant this
	// refuses), so this guard is what stops a Grant reaching this request
	// through Answer instead, once #670 exposes it.
	if frq.RequestStatus.RequestStatus == sep2.RequestStatusCancelled {
		return sep2.FlowReservationResponse{}, ErrRequestCancelled
	}

	interval, err := grantedInterval(frq, decision.Interval)
	if err != nil {
		return sep2.FlowReservationResponse{}, err
	}
	// A granted interval of duration zero is indistinguishable on the wire
	// from a denial (10.9.3.2), whether that zero came from the operator's
	// own override or from echoing a request that asked for a zero-length
	// window: either way a Grant must never produce it.
	if interval != nil && interval.Duration == 0 {
		return sep2.FlowReservationResponse{}, ErrGrantZeroDuration
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
	if want.Value != 0 && sign64(want.Value) != sign64(frq.EnergyRequested.Value) {
		return nil, ErrEnergyReversesRequest
	}
	if magnitudeExceeds(want.Value, want.Multiplier, frq.EnergyRequested.Value, frq.EnergyRequested.Multiplier) {
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
	if want.Value != 0 && sign64(int64(want.Value)) != sign64(int64(frq.PowerRequested.Value)) {
		return nil, ErrPowerReversesRequest
	}
	if magnitudeExceeds(int64(want.Value), want.Multiplier, int64(frq.PowerRequested.Value), frq.PowerRequested.Multiplier) {
		return nil, ErrPowerExceedsRequest
	}
	cp := *want
	return &cp, nil
}

// magnitudeExceeds reports whether |aVal*10^aMul| > |bVal*10^bMul|, computed
// exactly with math/big rather than float64. float64 has 53 bits of integer
// precision: a granted value of 2^53+1 against a requested 2^53 rounds to
// equal in float64 and passes a magnitude check that should refuse it. Both
// sides are brought to the smaller of the two multipliers by scaling the
// other UP with an integer power of ten, so the comparison never divides
// and never loses a digit.
func magnitudeExceeds(aVal int64, aMul int8, bVal int64, bMul int8) bool {
	a := new(big.Int).Abs(big.NewInt(aVal))
	b := new(big.Int).Abs(big.NewInt(bVal))
	switch {
	case aMul > bMul:
		a.Mul(a, pow10(int64(aMul)-int64(bMul)))
	case bMul > aMul:
		b.Mul(b, pow10(int64(bMul)-int64(aMul)))
	}
	return a.Cmp(b) > 0
}

func pow10(exp int64) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(exp), nil)
}

func sign64(v int64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}
