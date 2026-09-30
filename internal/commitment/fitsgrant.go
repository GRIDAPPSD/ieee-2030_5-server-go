package commitment

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
// each execution's mode, fleet, window and direction, then the aggregate
// power and energy bounds across the whole set.
//
// execs is the complete set to validate, not a delta: CheckControl (design
// 3.4) calls this with the grant's live executions plus the new proposal,
// and Revise calls it with the old grant's executions against the new
// grant. Neither call shape is visible to FitsGrant itself, so an aggregate
// conflict (rules 6 and 7) is never named by slice position; see checkPower
// and checkEnergy.
func FitsGrant(g Grant, execs []Control) error {
	if g.CancelledAt != nil {
		return &ConflictError{Code: ConflictGrantNotLive, MRID: g.MRID}
	}
	if g.Window == nil || g.Window.Duration == 0 || g.Energy == nil || g.Energy.Value == 0 || g.Power == nil {
		return &ConflictError{Code: ConflictNotExecutable, MRID: g.MRID}
	}

	want := DirectionOf(g)
	for _, c := range execs {
		if c.TargetW == nil {
			return &ConflictError{Code: ConflictModeNotTarget, MRID: c.MRID}
		}
		if c.FleetKey != g.FleetKey {
			return &ConflictError{Code: ConflictOutsideFleet, MRID: c.MRID}
		}
		if !c.Window.Within(*g.Window) {
			return &ConflictError{Code: ConflictOutsideInterval, MRID: c.MRID}
		}
		if sign(int64(c.TargetW.Value)) != want {
			return &ConflictError{Code: ConflictDirection, MRID: c.MRID}
		}
	}

	if err := checkPower(g, execs); err != nil {
		return err
	}
	return checkEnergy(g, execs)
}

// checkPower enforces rule 6: at every instant covered by some execution's
// window, the summed per-device power (|opModTargetW| x Reach) across every
// execution active at that instant must not exceed |powerAvailable|. The
// value is piecewise constant, so a local maximum can only appear where
// some execution's window begins (design: "exact for step functions"),
// which is why only those instants are tested.
//
// Design 5.3 names the conflict by "the other execution's" mRID, not the
// grant's. Here "the other execution" is whichever active execution is not
// the one whose own instant triggered the check (index i): that is the one
// rule 6's own comment frames as the caller's proposal in CheckControl's
// usual shape, but FitsGrant is handed a flat, order-agnostic set, so the
// rule is applied structurally rather than by slice position. When i is
// the only execution active at the violating instant, there is no other
// execution to name, and the conflict names the grant instead.
func checkPower(g Grant, execs []Control) error {
	for i, x := range execs {
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
			if j != i {
				return &ConflictError{Code: ConflictPower, MRID: execs[j].MRID}
			}
		}
		return &ConflictError{Code: ConflictPower, MRID: g.MRID}
	}
	return nil
}

// checkEnergy enforces rule 7: the summed energy of every execution
// (|opModTargetW| x Reach x duration, watt-seconds) must not exceed
// |energyAvailable| (watt-hours, converted to watt-seconds by scaling the
// bound with the same math/big machinery the sum uses, never by dividing
// the sum or multiplying the bound in plain int64, which could overflow
// for a value near the Int48 range design 5.3 allows). Design 5.3 names an
// energy conflict by the grant's mRID unconditionally: unlike power, there
// is no "other execution" reading for a bound every live execution
// contributes to at once, regardless of instant.
func checkEnergy(g Grant, execs []Control) error {
	terms := make([]scaledTerm, 0, len(execs))
	for _, c := range execs {
		terms = append(terms, scaledTerm{
			value:      int64(c.TargetW.Value),
			multiplier: c.TargetW.Multiplier,
			factor:     int64(c.Reach) * int64(c.Window.Duration),
		})
	}
	bound := scaledTerm{value: g.Energy.Value, multiplier: g.Energy.Multiplier, factor: 3600}
	if magnitudeSumExceeds(terms, bound) {
		return &ConflictError{Code: ConflictEnergy, MRID: g.MRID}
	}
	return nil
}
