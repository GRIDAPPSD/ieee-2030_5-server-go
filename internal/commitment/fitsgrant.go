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
// 3.4) calls this with the grant's live executions plus the new proposal
// appended last, and Revise calls it with the old grant's executions
// against the new grant. When an aggregate bound (power or energy) is
// broken, the conflict names the last element of execs, since that is
// always the one the caller is asking about.
func FitsGrant(g Grant, execs []Control) error {
	if g.CancelledAt != nil {
		return &ConflictError{Code: ConflictGrantNotLive, MRID: g.MRID}
	}
	if g.Window == nil || g.Energy == nil || g.Energy.Value == 0 || g.Power == nil {
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
func checkPower(g Grant, execs []Control) error {
	for _, instant := range execs {
		t := instant.Window.Start
		var terms []scaledTerm
		for _, c := range execs {
			if c.Window.Start <= t && t < c.Window.End() {
				terms = append(terms, scaledTerm{
					value:      int64(c.TargetW.Value),
					multiplier: c.TargetW.Multiplier,
					factor:     int64(c.Reach),
				})
			}
		}
		if magnitudeSumExceeds(terms, int64(g.Power.Value), g.Power.Multiplier) {
			return &ConflictError{Code: ConflictPower, MRID: execs[len(execs)-1].MRID}
		}
	}
	return nil
}

// checkEnergy enforces rule 7: the summed energy of every execution
// (|opModTargetW| x Reach x duration, watt-seconds) must not exceed
// |energyAvailable| (watt-hours, so the bound is scaled by 3600 rather
// than the sum divided, which would round).
func checkEnergy(g Grant, execs []Control) error {
	terms := make([]scaledTerm, 0, len(execs))
	for _, c := range execs {
		terms = append(terms, scaledTerm{
			value:      int64(c.TargetW.Value),
			multiplier: c.TargetW.Multiplier,
			factor:     int64(c.Reach) * int64(c.Window.Duration),
		})
	}
	if magnitudeSumExceeds(terms, g.Energy.Value*3600, g.Energy.Multiplier) {
		return &ConflictError{Code: ConflictEnergy, MRID: execs[len(execs)-1].MRID}
	}
	return nil
}
