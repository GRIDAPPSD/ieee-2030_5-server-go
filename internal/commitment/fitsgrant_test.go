package commitment

import (
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// baseGrant returns a live, fully executable charging grant: fleet FLEET1,
// window [1000, 4600), energy +10000 Wh (charging, our declared response
// convention), power 4000 W available.
func baseGrant() Grant {
	return Grant{
		MRID:        "grant-1",
		EndDeviceID: "edev-agg",
		FleetKey:    "FLEET1",
		Window:      &Window{Start: 1000, Duration: 3600},
		Energy:      &sep2.SignedRealEnergy{Multiplier: 0, Value: 10000},
		Power:       &sep2.ActivePower{Multiplier: 0, Value: 4000},
	}
}

// baseExecution returns a control that carries out baseGrant() cleanly: a
// charging grant executes as a negative opModTargetW (discharge-positive
// control convention, 2023 13524).
func baseExecution() Control {
	return Control{
		MRID:      "ctrl-1",
		FleetKey:  "FLEET1",
		Window:    Window{Start: 1000, Duration: 1800},
		GrantMRID: "grant-1",
		TargetW:   &sep2.ActivePower{Multiplier: 0, Value: -1000},
		Reach:     1,
	}
}

func wantConflict(t *testing.T, err error, wantCode ConflictCode, wantMRID string) {
	t.Helper()
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("FitsGrant error = %v (%T), want *ConflictError", err, err)
	}
	if ce.Code != wantCode {
		t.Errorf("ConflictError.Code = %q, want %q", ce.Code, wantCode)
	}
	if ce.MRID != wantMRID {
		t.Errorf("ConflictError.MRID = %q, want %q", ce.MRID, wantMRID)
	}
}

func TestFitsGrant_BaselineAccepts(t *testing.T) {
	t.Parallel()
	c := baseExecution()
	if err := FitsGrant(baseGrant(), nil, &c); err != nil {
		t.Fatalf("FitsGrant(valid grant, valid execution) error = %v, want nil", err)
	}
}

func TestFitsGrant_NoExecutionsAccepts(t *testing.T) {
	t.Parallel()
	if err := FitsGrant(baseGrant(), nil, nil); err != nil {
		t.Fatalf("FitsGrant(valid grant, no executions) error = %v, want nil", err)
	}
}

// Rule 1: the grant must be live and executable.
func TestFitsGrant_Rule1_GrantNotLive(t *testing.T) {
	t.Parallel()
	g := baseGrant()
	cancelled := int64(500)
	g.CancelledAt = &cancelled
	c := baseExecution()
	err := FitsGrant(g, nil, &c)
	wantConflict(t, err, ConflictGrantNotLive, g.MRID)
}

func TestFitsGrant_Rule1_GrantNotExecutable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(g *Grant)
	}{
		{"no window", func(g *Grant) { g.Window = nil }},
		{"zero duration window", func(g *Grant) { g.Window = &Window{Start: 1000, Duration: 0} }},
		{"no energy", func(g *Grant) { g.Energy = nil }},
		{"zero energy", func(g *Grant) { g.Energy = &sep2.SignedRealEnergy{Value: 0} }},
		{"no power", func(g *Grant) { g.Power = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := baseGrant()
			tc.mutate(&g)
			err := FitsGrant(g, nil, nil)
			wantConflict(t, err, ConflictNotExecutable, g.MRID)
		})
	}
}

// design 5.2: "a response with interval duration above zero" is what makes
// a grant live. Duration 0 refuses even with no executions; duration 1
// (otherwise identical) is accepted.
func TestFitsGrant_Rule1_WindowDurationBoundary(t *testing.T) {
	t.Parallel()
	t.Run("duration 0 is not live", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		g.Window = &Window{Start: 1000, Duration: 0}
		err := FitsGrant(g, nil, nil)
		wantConflict(t, err, ConflictNotExecutable, g.MRID)
	})
	t.Run("duration 1 is live", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		g.Window = &Window{Start: 1000, Duration: 1}
		if err := FitsGrant(g, nil, nil); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})
}

// Rule 2: the control must set opModTargetW and nothing else.
func TestFitsGrant_Rule2_ModeNotTarget(t *testing.T) {
	t.Parallel()
	c := baseExecution()
	c.TargetW = nil
	err := FitsGrant(baseGrant(), nil, &c)
	wantConflict(t, err, ConflictModeNotTarget, c.MRID)
}

// Rule 3: the execution's fleet key must equal the grant's.
func TestFitsGrant_Rule3_OutsideFleet(t *testing.T) {
	t.Parallel()
	c := baseExecution()
	c.FleetKey = "FLEET2"
	err := FitsGrant(baseGrant(), nil, &c)
	wantConflict(t, err, ConflictOutsideFleet, c.MRID)
}

// The zero-duration guard sits between rule 3 and rule 4, and fires only
// for the proposal (design 5.1): an existing live execution already
// clipped to zero duration (design 5.2) still counts as nothing.
func TestFitsGrant_ZeroDurationProposal(t *testing.T) {
	t.Parallel()
	g := baseGrant() // window [1000, 4600)

	t.Run("duration 0 at the grant's start is refused", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.Window = Window{Start: g.Window.Start, Duration: 0}
		err := FitsGrant(g, nil, &c)
		wantConflict(t, err, ConflictZeroDuration, c.MRID)
	})

	t.Run("duration 0 far outside the grant's window is refused the same way, not as outside-interval", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.Window = Window{Start: 999999, Duration: 0}
		err := FitsGrant(g, nil, &c)
		wantConflict(t, err, ConflictZeroDuration, c.MRID)
	})

	t.Run("duration 1 inside is accepted", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.Window = Window{Start: g.Window.Start, Duration: 1}
		if err := FitsGrant(g, nil, &c); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("an existing execution clipped to zero duration still counts as nothing, unrefused", func(t *testing.T) {
		t.Parallel()
		clipped := baseExecution()
		clipped.MRID = "ctrl-clipped"
		clipped.Window = Window{Start: 999999, Duration: 0} // nowhere near the grant's window
		proposal := baseExecution()
		proposal.MRID = "ctrl-proposal"
		if err := FitsGrant(g, []Control{clipped}, &proposal); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil: a zero-duration EXISTING execution must not be refused", err)
		}
	})
}

// Rule 4: the execution's window must lie within the grant's, both sides.
func TestFitsGrant_Rule4_WindowWithin(t *testing.T) {
	t.Parallel()
	g := baseGrant()

	t.Run("one step inside: exact match on the grant window is accepted", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.Window = *g.Window
		if err := FitsGrant(g, nil, &c); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("one step past: one second before the grant's start is refused", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.Window = Window{Start: g.Window.Start - 1, Duration: g.Window.Duration}
		err := FitsGrant(g, nil, &c)
		wantConflict(t, err, ConflictOutsideInterval, c.MRID)
	})

	t.Run("one step past: one second beyond the grant's end is refused", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.Window = Window{Start: g.Window.Start, Duration: g.Window.Duration + 1}
		err := FitsGrant(g, nil, &c)
		wantConflict(t, err, ConflictOutsideInterval, c.MRID)
	})
}

// Rule 5: direction. Charging (+) energy executes as a negative target;
// discharging (-) energy executes as a positive target. A zero target is
// always refused.
func TestFitsGrant_Rule5_Direction(t *testing.T) {
	t.Parallel()
	t.Run("charging grant accepts a negative target", func(t *testing.T) {
		t.Parallel()
		g := baseGrant() // energy +10000
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -2000}
		if err := FitsGrant(g, nil, &c); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("charging grant refuses a positive target", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: 2000}
		err := FitsGrant(g, nil, &c)
		wantConflict(t, err, ConflictDirection, c.MRID)
	})

	t.Run("discharging grant accepts a positive target", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		g.Energy = &sep2.SignedRealEnergy{Value: -10000}
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: 2000}
		if err := FitsGrant(g, nil, &c); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("discharging grant refuses a negative target", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		g.Energy = &sep2.SignedRealEnergy{Value: -10000}
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -2000}
		err := FitsGrant(g, nil, &c)
		wantConflict(t, err, ConflictDirection, c.MRID)
	})

	t.Run("a zero target is always refused", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: 0}
		err := FitsGrant(g, nil, &c)
		wantConflict(t, err, ConflictDirection, c.MRID)
	})
}

// sign is DirectionOf's building block. Pin its boundary explicitly: the
// smallest nonzero magnitudes on each side, not only the large ones
// DirectionOf's own tests happen to use.
func TestSign(t *testing.T) {
	t.Parallel()
	tests := []struct {
		v    int64
		want int
	}{
		{1, 1},
		{-1, -1},
		{0, 0},
	}
	for _, tc := range tests {
		if got := sign(tc.v); got != tc.want {
			t.Errorf("sign(%d) = %d, want %d", tc.v, got, tc.want)
		}
	}
}

// Rule 6: summed per-device power at every instant must not exceed
// powerAvailable. A conflict names an active execution other than the
// proposal, or the grant when the proposal is the only one active.
func TestFitsGrant_Rule6_Power(t *testing.T) {
	t.Parallel()
	g := baseGrant()
	g.Power = &sep2.ActivePower{Value: 4000}
	// Keep energy far out of the way so rule 7 never fires first.
	g.Energy = &sep2.SignedRealEnergy{Value: 1000000}

	t.Run("one step inside: exactly at the power bound is accepted", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -4000}
		c.Reach = 1
		if err := FitsGrant(g, nil, &c); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("one step past: one watt over the bound, alone, names the grant", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -4001}
		c.Reach = 1
		err := FitsGrant(g, nil, &c)
		wantConflict(t, err, ConflictPower, g.MRID)
	})

	t.Run("Reach multiplies the per-control contribution: one step past names the grant", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -1001}
		c.Reach = 4 // 1001 * 4 = 4004 > 4000
		err := FitsGrant(g, nil, &c)
		wantConflict(t, err, ConflictPower, g.MRID)
	})

	t.Run("Reach multiplies the per-control contribution: one step inside is accepted", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -1000}
		c.Reach = 4 // 1000 * 4 = 4000, exactly the bound
		if err := FitsGrant(g, nil, &c); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("four at a quarter of powerAvailable are accepted", func(t *testing.T) {
		t.Parallel()
		gg := baseGrant()
		gg.Power = &sep2.ActivePower{Value: 12000}
		gg.Energy = &sep2.SignedRealEnergy{Value: 1000000}
		var accepted []Control
		for i := 0; i < 4; i++ {
			c := baseExecution()
			c.MRID = mustMRID(i)
			c.Window = Window{Start: 1000, Duration: 10}
			c.TargetW = &sep2.ActivePower{Value: -3000} // 12000/4
			c.Reach = 1
			if err := FitsGrant(gg, accepted, &c); err != nil {
				t.Fatalf("FitsGrant for execution %d error = %v, want nil", i+1, err)
			}
			accepted = append(accepted, c)
		}
	})

	t.Run("four at a third of powerAvailable are refused on the fourth call, naming an existing execution, never the proposal", func(t *testing.T) {
		t.Parallel()
		gg := baseGrant()
		gg.Power = &sep2.ActivePower{Value: 12000}
		gg.Energy = &sep2.SignedRealEnergy{Value: 1000000}
		var accepted []Control
		for i := 0; i < 3; i++ {
			c := baseExecution()
			c.MRID = mustMRID(i)
			c.Window = Window{Start: 1000, Duration: 10}
			c.TargetW = &sep2.ActivePower{Value: -4000} // 12000/3
			c.Reach = 1
			if err := FitsGrant(gg, accepted, &c); err != nil {
				t.Fatalf("FitsGrant for execution %d error = %v, want nil", i+1, err)
			}
			accepted = append(accepted, c)
		}
		fourth := baseExecution()
		fourth.MRID = mustMRID(3)
		fourth.Window = Window{Start: 1000, Duration: 10}
		fourth.TargetW = &sep2.ActivePower{Value: -4000}
		fourth.Reach = 1
		err := FitsGrant(gg, accepted, &fourth)
		wantConflict(t, err, ConflictPower, mustMRID(0)) // the first of the three existing executions
	})

	// The proposal is never named: design 5.3's "other execution" reading.
	// Tested with the long execution as the proposal, and again with it as
	// the existing one, so the exclusion follows the identity given to
	// FitsGrant rather than which physical control happens to be larger
	// or smaller.
	t.Run("the proposal is never named, whichever control plays that role", func(t *testing.T) {
		t.Parallel()
		short := baseExecution()
		short.MRID = "ctrl-short"
		short.Window = Window{Start: 1050, Duration: 10} // [1050, 1060)
		short.TargetW = &sep2.ActivePower{Value: -4000}

		long := baseExecution()
		long.MRID = "ctrl-long"
		long.Window = Window{Start: 1000, Duration: 100} // [1000, 1100), spans short's start
		long.TargetW = &sep2.ActivePower{Value: -4000}

		t.Run("long is the proposal: the conflict names short, the existing one", func(t *testing.T) {
			t.Parallel()
			err := FitsGrant(g, []Control{short}, &long)
			wantConflict(t, err, ConflictPower, short.MRID)
		})

		t.Run("short is the proposal: the conflict names long, the existing one", func(t *testing.T) {
			t.Parallel()
			err := FitsGrant(g, []Control{long}, &short)
			wantConflict(t, err, ConflictPower, long.MRID)
		})
	})

	// Staggered starts: mutants this section kills. The active-at-t test is
	// c.Window.Start <= t && t < c.Window.End(); dropping either bound
	// survives a same-start test suite. checkPower is exercised directly
	// here (proposalIndex -1: neither pure detection test cares which
	// element is a proposal), in both slice orders, since the mutants this
	// proves against are position-dependent by construction and the public
	// FitsGrant no longer exposes raw slice order to a caller.
	t.Run("staggered starts kill position-dependent mutants in both slice orders", func(t *testing.T) {
		t.Parallel()

		shortLong := func() (short, long Control) {
			short = baseExecution()
			short.MRID = "ctrl-short"
			short.Window = Window{Start: 1050, Duration: 10} // [1050, 1060)
			short.TargetW = &sep2.ActivePower{Value: -4000}
			long = baseExecution()
			long.MRID = "ctrl-long"
			long.Window = Window{Start: 1000, Duration: 100} // [1000, 1100)
			long.TargetW = &sep2.ActivePower{Value: -4000}
			return short, long
		}

		t.Run("short first, long second: kills checking only the first execution's start", func(t *testing.T) {
			t.Parallel()
			short, long := shortLong()
			// Under "check only execs[0]'s start" (t=1050), long is
			// already active there too (its window covers 1050), so this
			// order alone does not distinguish that mutant; it is here to
			// keep both orders symmetric with the pair below and to kill
			// "check only the last execution's start" (t=1000 alone would
			// miss the violation, since short is not active at 1000).
			if err := checkPower(g, []Control{short, long}, -1); err == nil {
				t.Fatal("checkPower error = nil, want *ConflictError")
			}
		})

		t.Run("long first, short second: kills checking only the first execution's start", func(t *testing.T) {
			t.Parallel()
			short, long := shortLong()
			// Checking only execs[0]'s start (long's, t=1000) finds only
			// long active (4000 W, not exceeding); the violation is only
			// visible at execs[1]'s start (short's, t=1050), where both
			// are active. A "first only" mutant accepts; full instant
			// coverage refuses.
			if err := checkPower(g, []Control{long, short}, -1); err == nil {
				t.Fatal("checkPower error = nil, want *ConflictError")
			}
		})
	})

	t.Run("touching windows never overlap in the power sense, in either order", func(t *testing.T) {
		t.Parallel()

		t.Run("later window second: kills dropping the lower/Start bound", func(t *testing.T) {
			t.Parallel()
			later := baseExecution()
			later.Window = Window{Start: 1010, Duration: 10} // [1010, 1020)
			later.TargetW = &sep2.ActivePower{Value: -4000}
			earlier := baseExecution()
			earlier.Window = Window{Start: 1000, Duration: 10} // [1000, 1010)
			earlier.TargetW = &sep2.ActivePower{Value: -4000}
			if err := checkPower(g, []Control{later, earlier}, -1); err != nil {
				t.Fatalf("checkPower(touching, non-overlapping windows) error = %v, want nil", err)
			}
		})

		t.Run("earlier window second: kills dropping the upper/End bound", func(t *testing.T) {
			t.Parallel()
			earlier := baseExecution()
			earlier.Window = Window{Start: 1000, Duration: 10} // [1000, 1010)
			earlier.TargetW = &sep2.ActivePower{Value: -4000}
			later := baseExecution()
			later.Window = Window{Start: 1010, Duration: 10} // [1010, 1020)
			later.TargetW = &sep2.ActivePower{Value: -4000}
			if err := checkPower(g, []Control{earlier, later}, -1); err != nil {
				t.Fatalf("checkPower(touching, non-overlapping windows) error = %v, want nil", err)
			}
		})
	})

	// Multipliers: every FitsGrant test above uses multiplier 0. These pin
	// TargetW's and Power's multiplier each on their own, so zeroing
	// either in checkPower fails the corresponding case.
	t.Run("multipliers", func(t *testing.T) {
		t.Parallel()

		t.Run("a kW-scaled target exceeds a watt-scale bound", func(t *testing.T) {
			t.Parallel()
			gg := baseGrant()
			gg.Power = &sep2.ActivePower{Value: 4, Multiplier: 3} // 4000 W
			gg.Energy = &sep2.SignedRealEnergy{Value: 1000000}
			c := baseExecution()
			c.TargetW = &sep2.ActivePower{Value: -5, Multiplier: 3} // 5000 W > 4000 W
			c.Reach = 1
			err := FitsGrant(gg, nil, &c)
			wantConflict(t, err, ConflictPower, gg.MRID)
		})

		t.Run("a kW-scaled bound admits a watt-scale target under it", func(t *testing.T) {
			t.Parallel()
			gg := baseGrant()
			gg.Power = &sep2.ActivePower{Value: 4, Multiplier: 3} // 4000 W
			gg.Energy = &sep2.SignedRealEnergy{Value: 1000000}
			c := baseExecution()
			c.TargetW = &sep2.ActivePower{Value: -3999} // 3999 W < 4000 W
			c.Reach = 1
			if err := FitsGrant(gg, nil, &c); err != nil {
				t.Fatalf("FitsGrant error = %v, want nil", err)
			}
		})
	})
}

// Rule 7: summed energy across the grant's live executions must not
// exceed energyAvailable, converted watt-hours to watt-seconds. A
// conflict always names the grant: unlike power, there is no "other"
// execution reading for a bound every execution contributes to at once.
func TestFitsGrant_Rule7_Energy(t *testing.T) {
	t.Parallel()
	g := baseGrant()
	g.Energy = &sep2.SignedRealEnergy{Value: 10} // 10 Wh = 36000 Ws
	g.Power = &sep2.ActivePower{Value: 30000}    // keep rule 6 out of the way

	t.Run("one step inside: exactly at the energy bound is accepted", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -100}
		c.Reach = 1
		c.Window = Window{Start: 1000, Duration: 360} // 100 * 1 * 360 = 36000 Ws
		if err := FitsGrant(g, nil, &c); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("one step past: one second over the bound names the grant", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -100}
		c.Reach = 1
		c.Window = Window{Start: 1000, Duration: 361} // 36100 Ws > 36000 Ws
		err := FitsGrant(g, nil, &c)
		wantConflict(t, err, ConflictEnergy, g.MRID)
	})

	t.Run("Reach", func(t *testing.T) {
		t.Parallel()
		t.Run("one step inside: Reach scales the energy exactly to the bound", func(t *testing.T) {
			t.Parallel()
			c := baseExecution()
			c.TargetW = &sep2.ActivePower{Value: -50}
			c.Reach = 2
			c.Window = Window{Start: 1000, Duration: 360} // 50*2*360 = 36000 Ws
			if err := FitsGrant(g, nil, &c); err != nil {
				t.Fatalf("FitsGrant error = %v, want nil", err)
			}
		})
		t.Run("one step past: Reach pushes the energy one second's worth over", func(t *testing.T) {
			t.Parallel()
			c := baseExecution()
			c.TargetW = &sep2.ActivePower{Value: -50}
			c.Reach = 2
			c.Window = Window{Start: 1000, Duration: 361} // 50*2*361 = 36100 Ws
			err := FitsGrant(g, nil, &c)
			wantConflict(t, err, ConflictEnergy, g.MRID)
		})
	})

	// Multipliers: pin TargetW's and Energy's multiplier each on their
	// own, so zeroing either in checkEnergy fails the corresponding case.
	t.Run("multipliers", func(t *testing.T) {
		t.Parallel()

		t.Run("a kW-scaled target exceeds a watt-hour-scale energy bound", func(t *testing.T) {
			t.Parallel()
			gg := baseGrant()
			gg.Power = &sep2.ActivePower{Value: 30000}
			gg.Energy = &sep2.SignedRealEnergy{Value: 10} // 36000 Ws
			c := baseExecution()
			c.TargetW = &sep2.ActivePower{Value: -2, Multiplier: 2} // 200 W
			c.Reach = 1
			c.Window = Window{Start: 1000, Duration: 200} // 200*200 = 40000 Ws > 36000
			err := FitsGrant(gg, nil, &c)
			wantConflict(t, err, ConflictEnergy, gg.MRID)
		})

		t.Run("a kilowatt-hour-scaled bound admits a watt-scale target under it", func(t *testing.T) {
			t.Parallel()
			gg := baseGrant()
			gg.Power = &sep2.ActivePower{Value: 30000}
			gg.Energy = &sep2.SignedRealEnergy{Value: 1, Multiplier: 1} // 10 Wh = 36000 Ws
			c := baseExecution()
			c.TargetW = &sep2.ActivePower{Value: -99}
			c.Reach = 1
			c.Window = Window{Start: 1000, Duration: 360} // 99*360 = 35640 Ws < 36000
			if err := FitsGrant(gg, nil, &c); err != nil {
				t.Fatalf("FitsGrant error = %v, want nil", err)
			}
		})
	})

	// Pins the 3600 Wh-to-Ws constant itself: 1 Wh = 3600 Ws exactly, and
	// *3601 (or any nearby constant) would accept what this refuses.
	t.Run("the 3600 constant", func(t *testing.T) {
		t.Parallel()
		gg := baseGrant()
		gg.Power = &sep2.ActivePower{Value: 30000}
		gg.Energy = &sep2.SignedRealEnergy{Value: 1}      // 1 Wh = 3600 Ws
		gg.Window = &Window{Start: 1000, Duration: 10000} // wide enough to hold a 3601s execution (rule 4)

		t.Run("3600 Ws exactly is accepted", func(t *testing.T) {
			t.Parallel()
			c := baseExecution()
			c.TargetW = &sep2.ActivePower{Value: -1}
			c.Reach = 1
			c.Window = Window{Start: 1000, Duration: 3600}
			if err := FitsGrant(gg, nil, &c); err != nil {
				t.Fatalf("FitsGrant error = %v, want nil", err)
			}
		})
		t.Run("3601 Ws is refused: a 3601 constant would wrongly accept it", func(t *testing.T) {
			t.Parallel()
			c := baseExecution()
			c.TargetW = &sep2.ActivePower{Value: -1}
			c.Reach = 1
			c.Window = Window{Start: 1000, Duration: 3601}
			err := FitsGrant(gg, nil, &c)
			wantConflict(t, err, ConflictEnergy, gg.MRID)
		})
	})
}

func mustMRID(i int) string {
	names := []string{"ctrl-a", "ctrl-b", "ctrl-c", "ctrl-d", "ctrl-e"}
	return names[i]
}

func TestDirectionOf(t *testing.T) {
	t.Parallel()
	charging := baseGrant()
	charging.Energy = &sep2.SignedRealEnergy{Value: 10000}
	if got := DirectionOf(charging); got != -1 {
		t.Errorf("DirectionOf(charging grant) = %d, want -1", got)
	}

	discharging := baseGrant()
	discharging.Energy = &sep2.SignedRealEnergy{Value: -10000}
	if got := DirectionOf(discharging); got != 1 {
		t.Errorf("DirectionOf(discharging grant) = %d, want 1", got)
	}
}
