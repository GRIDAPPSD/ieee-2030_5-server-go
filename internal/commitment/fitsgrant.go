package commitment

import "math/big"

// DirectionOf returns the sign opModTargetW must carry to execute g:
// the negative of energyAvailable's sign. The response stores energy in
// our declared convention, charging positive (2023 13523-13524 states no
// sign for energyAvailable; internal/flowreservation pins charging
// positive as ours); the DER control's opModTargetW is discharge-positive
// (2023 13524), so a charging grant executes as a negative target. The
// caller must have already confirmed g.Energy is non-nil and non-zero
// (FitsGrant's rule 1 does, before ever reaching a direction check).
func DirectionOf(g Grant) int {
	return -sign(g.Energy.Value)
}

func sign(v int64) int {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	default:
		return 0
	}
}

// FitsGrant checks every rule of design 5.3 for a set of executions against
// one grant, and returns the first ConflictError found. Rules are checked
// in the order 5.3 lists them: grant liveness and executability once, then
// each execution's mode, fleet, zero-duration guard, window and direction,
// then the aggregate power and energy bounds across the whole set.
//
// existing is the grant's current live executions. proposal is the one
// execution under test, or nil when there is none: CheckControl (design
// 3.4) passes the grant's live executions as existing and the new control
// as proposal; Revise passes the old grant's executions as existing with a
// nil proposal, since it re-validates a fixed set rather than admitting a
// new one. The distinction matters for two rules: a zero-duration proposal
// is refused outright (design 5.1), while a zero-duration member of
// existing (already clipped at SupersededAt, design 5.2) still counts as
// nothing; and an aggregate conflict (rule 6) never names the proposal,
// which is design 5.3's "other execution" reading, never a slice position.
func FitsGrant(g Grant, existing []Control, proposal *Control) error {
	if g.CancelledAt != nil {
		return &ConflictError{Code: ConflictGrantNotLive, MRID: g.MRID}
	}
	if g.Window == nil || g.Window.Duration == 0 || g.Energy == nil || g.Energy.Value == 0 || g.Power == nil {
		return &ConflictError{Code: ConflictNotExecutable, MRID: g.MRID}
	}

	all := make([]Control, 0, len(existing)+1)
	all = append(all, existing...)
	proposalIndex := -1
	if proposal != nil {
		proposalIndex = len(all)
		all = append(all, *proposal)
	}

	want := DirectionOf(g)
	for j, c := range all {
		if c.TargetW == nil {
			return &ConflictError{Code: ConflictModeNotTarget, MRID: c.MRID}
		}
		if c.FleetKey != g.FleetKey {
			return &ConflictError{Code: ConflictOutsideFleet, MRID: c.MRID}
		}
		if j == proposalIndex && c.Window.Duration == 0 {
			return &ConflictError{Code: ConflictZeroDuration, MRID: c.MRID}
		}
		if !c.Window.Within(*g.Window) {
			return &ConflictError{Code: ConflictOutsideInterval, MRID: c.MRID}
		}
		if sign(int64(c.TargetW.Value)) != want {
			return &ConflictError{Code: ConflictDirection, MRID: c.MRID}
		}
	}

	if err := checkPower(g, all, proposalIndex); err != nil {
		return err
	}
	return checkEnergy(g, all)
}

// checkPower enforces rule 6: at every instant covered by some execution's
// window, the summed per-device power (|opModTargetW| x Reach) across every
// execution active at that instant must not exceed |powerAvailable|. The
// value is piecewise constant, so a local maximum can only appear where
// some execution's window begins (design: "exact for step functions"),
// which is why only those instants, for every execution in execs, are
// tested: checking only the first or only the last survives a same-start
// test suite and misses a violation whose sole witnessing instant is
// elsewhere in the set.
//
// Design 5.3 names the conflict by "the other execution's" mRID, never the
// proposal's (proposalIndex, -1 when there is none): the first active
// execution other than the proposal, in execs order. When the proposal is
// the only execution active at the violating instant, there is no other
// execution to name, and the conflict names the grant instead.
func checkPower(g Grant, execs []Control, proposalIndex int) error {
	for _, x := range execs {
		t := x.Window.Start
		var active []int
		for j, c := range execs {
			if c.Window.Start <= t && t < c.Window.End() {
				active = append(active, j)
			}
		}

		var terms []scaledTerm
		for _, j := range active {
			c := execs[j]
			terms = append(terms, scaledTerm{
				value:      int64(c.TargetW.Value),
				multiplier: c.TargetW.Multiplier,
				factor:     int64(c.Reach),
			})
		}
		if !magnitudeSumExceeds(terms, scaledTerm{value: int64(g.Power.Value), multiplier: g.Power.Multiplier, factor: 1}) {
			continue
		}

		for _, j := range active {
			if j != proposalIndex {
				return &ConflictError{Code: ConflictPower, MRID: execs[j].MRID}
			}
		}
		return &ConflictError{Code: ConflictPower, MRID: g.MRID}
	}
	return nil
}

// checkEnergy enforces rule 7: the energy Committed to the executions must
// not exceed |energyAvailable|, watt-hours converted to watt-seconds exactly.
// Design 5.3 names an energy conflict by the grant's mRID unconditionally:
// unlike power, there is no "other execution" reading for a bound every live
// execution contributes to at once, regardless of instant.
func checkEnergy(g Grant, execs []Control) error {
	bound := ratScaled(g.Energy.Value, g.Energy.Multiplier, 3600)
	if Committed(execs).Cmp(bound) > 0 {
		return &ConflictError{Code: ConflictEnergy, MRID: g.MRID}
	}
	return nil
}

// Committed is the energy execs carry out, in watt-seconds: the sum of
// |opModTargetW| x Reach x duration over every control that has a target.
// It is the one figure rule 7 compares with the grant's energyAvailable and
// the admin view shows as energy committed, so the two cannot disagree. The
// caller passes the executions that count (live ones); a control without a
// target adds nothing, since FitsGrant refuses one before this is reached.
// It is a Rat because a negative multiplier makes the sum fractional.
func Committed(execs []Control) *big.Rat {
	sum := new(big.Rat)
	for _, c := range execs {
		if c.TargetW == nil {
			continue
		}
		sum.Add(sum, ratScaled(int64(c.TargetW.Value), c.TargetW.Multiplier, int64(c.Reach)*int64(c.Window.Duration)))
	}
	return sum
}
