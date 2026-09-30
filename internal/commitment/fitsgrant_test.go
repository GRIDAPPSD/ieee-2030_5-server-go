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
	if err := FitsGrant(baseGrant(), []Control{baseExecution()}); err != nil {
		t.Fatalf("FitsGrant(valid grant, valid execution) error = %v, want nil", err)
	}
}

func TestFitsGrant_NoExecutionsAccepts(t *testing.T) {
	t.Parallel()
	if err := FitsGrant(baseGrant(), nil); err != nil {
		t.Fatalf("FitsGrant(valid grant, no executions) error = %v, want nil", err)
	}
}

// Rule 1: the grant must be live and executable.
func TestFitsGrant_Rule1_GrantNotLive(t *testing.T) {
	t.Parallel()
	g := baseGrant()
	cancelled := int64(500)
	g.CancelledAt = &cancelled
	err := FitsGrant(g, []Control{baseExecution()})
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
			err := FitsGrant(g, nil)
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
		err := FitsGrant(g, nil)
		wantConflict(t, err, ConflictNotExecutable, g.MRID)
	})
	t.Run("duration 1 is live", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		g.Window = &Window{Start: 1000, Duration: 1}
		if err := FitsGrant(g, nil); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})
}

// Rule 2: the control must set opModTargetW and nothing else.
func TestFitsGrant_Rule2_ModeNotTarget(t *testing.T) {
	t.Parallel()
	c := baseExecution()
	c.TargetW = nil
	err := FitsGrant(baseGrant(), []Control{c})
	wantConflict(t, err, ConflictModeNotTarget, c.MRID)
}

// Rule 3: the execution's fleet key must equal the grant's.
func TestFitsGrant_Rule3_OutsideFleet(t *testing.T) {
	t.Parallel()
	c := baseExecution()
	c.FleetKey = "FLEET2"
	err := FitsGrant(baseGrant(), []Control{c})
	wantConflict(t, err, ConflictOutsideFleet, c.MRID)
}

// Rule 4: the execution's window must lie within the grant's, both sides.
func TestFitsGrant_Rule4_WindowWithin(t *testing.T) {
	t.Parallel()
	g := baseGrant()

	t.Run("one step inside: exact match on the grant window is accepted", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.Window = *g.Window
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("one step past: one second before the grant's start is refused", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.Window = Window{Start: g.Window.Start - 1, Duration: g.Window.Duration}
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictOutsideInterval, c.MRID)
	})

	t.Run("one step past: one second beyond the grant's end is refused", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.Window = Window{Start: g.Window.Start, Duration: g.Window.Duration + 1}
		err := FitsGrant(g, []Control{c})
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
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("charging grant refuses a positive target", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: 2000}
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictDirection, c.MRID)
	})

	t.Run("discharging grant accepts a positive target", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		g.Energy = &sep2.SignedRealEnergy{Value: -10000}
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: 2000}
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("discharging grant refuses a negative target", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		g.Energy = &sep2.SignedRealEnergy{Value: -10000}
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -2000}
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictDirection, c.MRID)
	})

	t.Run("a zero target is always refused", func(t *testing.T) {
		t.Parallel()
		g := baseGrant()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: 0}
		err := FitsGrant(g, []Control{c})
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
// powerAvailable. A conflict names an execution active at the violating
// instant other than the one whose own instant triggered the check, or the
// grant when that execution is alone.
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
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("one step past: one watt over the bound, alone, names the grant", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -4001}
		c.Reach = 1
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictPower, g.MRID)
	})

	t.Run("Reach multiplies the per-control contribution: one step past names the grant", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -1001}
		c.Reach = 4 // 1001 * 4 = 4004 > 4000
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictPower, g.MRID)
	})

	t.Run("Reach multiplies the per-control contribution: one step inside is accepted", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -1000}
		c.Reach = 4 // 1000 * 4 = 4000, exactly the bound
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("four at a quarter of powerAvailable are accepted", func(t *testing.T) {
		t.Parallel()
		gg := baseGrant()
		gg.Power = &sep2.ActivePower{Value: 12000}
		gg.Energy = &sep2.SignedRealEnergy{Value: 1000000}
		var execs []Control
		for i := 0; i < 4; i++ {
			c := baseExecution()
			c.MRID = mustMRID(i)
			c.Window = Window{Start: 1000, Duration: 10}
			c.TargetW = &sep2.ActivePower{Value: -3000} // 12000/4
			c.Reach = 1
			execs = append(execs, c)
			if err := FitsGrant(gg, execs); err != nil {
				t.Fatalf("FitsGrant after adding execution %d error = %v, want nil", i+1, err)
			}
		}
	})

	t.Run("four at a third of powerAvailable are refused on the fourth call", func(t *testing.T) {
		t.Parallel()
		gg := baseGrant()
		gg.Power = &sep2.ActivePower{Value: 12000}
		gg.Energy = &sep2.SignedRealEnergy{Value: 1000000}
		var execs []Control
		for i := 0; i < 3; i++ {
			c := baseExecution()
			c.MRID = mustMRID(i)
			c.Window = Window{Start: 1000, Duration: 10}
			c.TargetW = &sep2.ActivePower{Value: -4000} // 12000/3
			c.Reach = 1
			execs = append(execs, c)
			if err := FitsGrant(gg, execs); err != nil {
				t.Fatalf("FitsGrant after accepted execution %d error = %v, want nil", i+1, err)
			}
		}
		fourth := baseExecution()
		fourth.MRID = mustMRID(3)
		fourth.Window = Window{Start: 1000, Duration: 10}
		fourth.TargetW = &sep2.ActivePower{Value: -4000}
		fourth.Reach = 1
		execs = append(execs, fourth)
		// All four share one window, so the violation is found at the
		// first execution's own instant (index 0); the conflict names
		// some other active execution, not index 0 itself: here, the
		// second control added (mustMRID(1)).
		err := FitsGrant(gg, execs)
		wantConflict(t, err, ConflictPower, mustMRID(1))
	})

	// Staggered starts: mutants this section kills. The active-at-t test is
	// c.Window.Start <= t && t < c.Window.End(); dropping either bound, or
	// checking only the last execution's own start instead of every
	// execution's, each survives a same-start test suite and is only
	// caught by windows that begin at different seconds.
	t.Run("staggered starts", func(t *testing.T) {
		t.Parallel()

		t.Run("the reviewer's case: a proposal spanning an existing execution's start must be refused (kills checking only the last execution's start)", func(t *testing.T) {
			t.Parallel()
			existing := baseExecution()
			existing.MRID = "ctrl-existing"
			existing.Window = Window{Start: 1050, Duration: 10}
			existing.TargetW = &sep2.ActivePower{Value: -4000}
			proposal := baseExecution()
			proposal.MRID = "ctrl-proposal"
			proposal.Window = Window{Start: 1000, Duration: 100}
			proposal.TargetW = &sep2.ActivePower{Value: -4000}
			// existing's own start (1050) is the only instant where both
			// are active; checking only the last element's start (1000)
			// would miss it and wrongly accept.
			err := FitsGrant(g, []Control{existing, proposal})
			wantConflict(t, err, ConflictPower, proposal.MRID)
		})

		t.Run("touching windows, later window second, do not overlap (kills dropping the lower/Start bound)", func(t *testing.T) {
			t.Parallel()
			later := baseExecution()
			later.MRID = "ctrl-later"
			later.Window = Window{Start: 1010, Duration: 10} // [1010, 1020)
			later.TargetW = &sep2.ActivePower{Value: -4000}
			earlier := baseExecution()
			earlier.MRID = "ctrl-earlier"
			earlier.Window = Window{Start: 1000, Duration: 10} // [1000, 1010)
			earlier.TargetW = &sep2.ActivePower{Value: -4000}
			// Dropping the Start bound would count "later" as already
			// active at "earlier"'s start-adjacent instant 1000 purely
			// because 1000 < later.End(); the two never actually share a
			// second.
			if err := FitsGrant(g, []Control{later, earlier}); err != nil {
				t.Fatalf("FitsGrant(touching, non-overlapping windows) error = %v, want nil", err)
			}
		})

		t.Run("touching windows, earlier window second, do not overlap (kills dropping the upper/End bound)", func(t *testing.T) {
			t.Parallel()
			earlier := baseExecution()
			earlier.MRID = "ctrl-earlier"
			earlier.Window = Window{Start: 1000, Duration: 10} // [1000, 1010)
			earlier.TargetW = &sep2.ActivePower{Value: -4000}
			later := baseExecution()
			later.MRID = "ctrl-later"
			later.Window = Window{Start: 1010, Duration: 10} // [1010, 1020)
			later.TargetW = &sep2.ActivePower{Value: -4000}
			// Dropping the End bound would count "earlier" as still
			// active at "later"'s start instant 1010 purely because
			// earlier.Start <= 1010, even though earlier's window ended
			// exactly then.
			if err := FitsGrant(g, []Control{earlier, later}); err != nil {
				t.Fatalf("FitsGrant(touching, non-overlapping windows) error = %v, want nil", err)
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
			err := FitsGrant(gg, []Control{c})
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
			if err := FitsGrant(gg, []Control{c}); err != nil {
				t.Fatalf("FitsGrant error = %v, want nil", err)
			}
		})
	})

	t.Run("an offender placed first still gets an other-execution name, and order does not change which rule fires", func(t *testing.T) {
		t.Parallel()
		gg := baseGrant()
		gg.Power = &sep2.ActivePower{Value: 4000}
		gg.Energy = &sep2.SignedRealEnergy{Value: 1000000}

		offender := baseExecution()
		offender.MRID = "ctrl-offender"
		offender.Window = Window{Start: 1000, Duration: 10}
		offender.TargetW = &sep2.ActivePower{Value: -4000}
		offender.Reach = 2 // 8000 W alone: already past the 4000 W bound

		innocent := baseExecution()
		innocent.MRID = "ctrl-innocent"
		innocent.Window = Window{Start: 1000, Duration: 10}
		innocent.TargetW = &sep2.ActivePower{Value: -10}
		innocent.Reach = 1

		// Offender first: the violation is found at index 0 (offender's
		// own instant), and the conflict names the other active
		// execution, innocent, not offender itself.
		err := FitsGrant(gg, []Control{offender, innocent})
		wantConflict(t, err, ConflictPower, innocent.MRID)

		// Innocent first: the violation is still found at index 0
		// (innocent's own instant, since both share one window), and the
		// conflict now names offender: the naming follows "other than the
		// index under test", never a fixed slice position.
		err = FitsGrant(gg, []Control{innocent, offender})
		wantConflict(t, err, ConflictPower, offender.MRID)
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
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("one step past: one second over the bound names the grant", func(t *testing.T) {
		t.Parallel()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -100}
		c.Reach = 1
		c.Window = Window{Start: 1000, Duration: 361} // 36100 Ws > 36000 Ws
		err := FitsGrant(g, []Control{c})
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
			if err := FitsGrant(g, []Control{c}); err != nil {
				t.Fatalf("FitsGrant error = %v, want nil", err)
			}
		})
		t.Run("one step past: Reach pushes the energy one second's worth over", func(t *testing.T) {
			t.Parallel()
			c := baseExecution()
			c.TargetW = &sep2.ActivePower{Value: -50}
			c.Reach = 2
			c.Window = Window{Start: 1000, Duration: 361} // 50*2*361 = 36100 Ws
			err := FitsGrant(g, []Control{c})
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
			err := FitsGrant(gg, []Control{c})
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
			if err := FitsGrant(gg, []Control{c}); err != nil {
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
			if err := FitsGrant(gg, []Control{c}); err != nil {
				t.Fatalf("FitsGrant error = %v, want nil", err)
			}
		})
		t.Run("3601 Ws is refused: a 3601 constant would wrongly accept it", func(t *testing.T) {
			t.Parallel()
			c := baseExecution()
			c.TargetW = &sep2.ActivePower{Value: -1}
			c.Reach = 1
			c.Window = Window{Start: 1000, Duration: 3601}
			err := FitsGrant(gg, []Control{c})
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
