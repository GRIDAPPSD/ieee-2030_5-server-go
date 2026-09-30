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
	if err := FitsGrant(baseGrant(), []Control{baseExecution()}); err != nil {
		t.Fatalf("FitsGrant(valid grant, valid execution) error = %v, want nil", err)
	}
}

func TestFitsGrant_NoExecutionsAccepts(t *testing.T) {
	if err := FitsGrant(baseGrant(), nil); err != nil {
		t.Fatalf("FitsGrant(valid grant, no executions) error = %v, want nil", err)
	}
}

// Rule 1: the grant must be live and executable.
func TestFitsGrant_Rule1_GrantNotLive(t *testing.T) {
	g := baseGrant()
	cancelled := int64(500)
	g.CancelledAt = &cancelled
	err := FitsGrant(g, []Control{baseExecution()})
	wantConflict(t, err, ConflictGrantNotLive, g.MRID)
}

func TestFitsGrant_Rule1_GrantNotExecutable(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(g *Grant)
	}{
		{"no window", func(g *Grant) { g.Window = nil }},
		{"no energy", func(g *Grant) { g.Energy = nil }},
		{"zero energy", func(g *Grant) { g.Energy = &sep2.SignedRealEnergy{Value: 0} }},
		{"no power", func(g *Grant) { g.Power = nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := baseGrant()
			tc.mutate(&g)
			err := FitsGrant(g, nil)
			wantConflict(t, err, ConflictNotExecutable, g.MRID)
		})
	}
}

// Rule 2: the control must set opModTargetW and nothing else.
func TestFitsGrant_Rule2_ModeNotTarget(t *testing.T) {
	c := baseExecution()
	c.TargetW = nil
	err := FitsGrant(baseGrant(), []Control{c})
	wantConflict(t, err, ConflictModeNotTarget, c.MRID)
}

// Rule 3: the execution's fleet key must equal the grant's.
func TestFitsGrant_Rule3_OutsideFleet(t *testing.T) {
	c := baseExecution()
	c.FleetKey = "FLEET2"
	err := FitsGrant(baseGrant(), []Control{c})
	wantConflict(t, err, ConflictOutsideFleet, c.MRID)
}

// Rule 4: the execution's window must lie within the grant's.
func TestFitsGrant_Rule4_WindowWithin(t *testing.T) {
	g := baseGrant()

	t.Run("one step inside: exact match on the grant window is accepted", func(t *testing.T) {
		c := baseExecution()
		c.Window = *g.Window
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("one step past: one second beyond the grant's end is refused", func(t *testing.T) {
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
	t.Run("charging grant accepts a negative target", func(t *testing.T) {
		g := baseGrant() // energy +10000
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -2000}
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("charging grant refuses a positive target", func(t *testing.T) {
		g := baseGrant()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: 2000}
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictDirection, c.MRID)
	})

	t.Run("discharging grant accepts a positive target", func(t *testing.T) {
		g := baseGrant()
		g.Energy = &sep2.SignedRealEnergy{Value: -10000}
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: 2000}
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("discharging grant refuses a negative target", func(t *testing.T) {
		g := baseGrant()
		g.Energy = &sep2.SignedRealEnergy{Value: -10000}
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -2000}
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictDirection, c.MRID)
	})

	t.Run("a zero target is always refused", func(t *testing.T) {
		g := baseGrant()
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: 0}
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictDirection, c.MRID)
	})
}

// Rule 6: summed per-device power at every instant must not exceed
// powerAvailable. The comment's own test: four executions at a quarter of
// powerAvailable are accepted; four at a third are refused on the fourth.
func TestFitsGrant_Rule6_Power(t *testing.T) {
	g := baseGrant()
	g.Power = &sep2.ActivePower{Value: 4000}
	// Keep energy far out of the way so rule 7 never fires first.
	g.Energy = &sep2.SignedRealEnergy{Value: 1000000}

	t.Run("one step inside: exactly at the power bound is accepted", func(t *testing.T) {
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -4000}
		c.Reach = 1
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("one step past: one watt over the bound is refused", func(t *testing.T) {
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -4001}
		c.Reach = 1
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictPower, c.MRID)
	})

	t.Run("Reach multiplies the per-control contribution", func(t *testing.T) {
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -1001}
		c.Reach = 4 // 1001 * 4 = 4004 > 4000
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictPower, c.MRID)
	})

	t.Run("four at a quarter of powerAvailable are accepted", func(t *testing.T) {
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

	t.Run("four at a third of powerAvailable are refused on the fourth", func(t *testing.T) {
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
		err := FitsGrant(gg, execs)
		wantConflict(t, err, ConflictPower, fourth.MRID)
	})
}

// Rule 7: summed energy across the grant's live executions must not
// exceed energyAvailable, converted watt-hours to watt-seconds.
func TestFitsGrant_Rule7_Energy(t *testing.T) {
	g := baseGrant()
	g.Energy = &sep2.SignedRealEnergy{Value: 10} // 10 Wh = 36000 Ws
	g.Power = &sep2.ActivePower{Value: 30000}    // keep rule 6 out of the way

	t.Run("one step inside: exactly at the energy bound is accepted", func(t *testing.T) {
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -100}
		c.Reach = 1
		c.Window = Window{Start: 1000, Duration: 360} // 100 * 1 * 360 = 36000 Ws
		if err := FitsGrant(g, []Control{c}); err != nil {
			t.Fatalf("FitsGrant error = %v, want nil", err)
		}
	})

	t.Run("one step past: one second over the bound is refused", func(t *testing.T) {
		c := baseExecution()
		c.TargetW = &sep2.ActivePower{Value: -100}
		c.Reach = 1
		c.Window = Window{Start: 1000, Duration: 361} // 36100 Ws > 36000 Ws
		err := FitsGrant(g, []Control{c})
		wantConflict(t, err, ConflictEnergy, c.MRID)
	})
}

func mustMRID(i int) string {
	names := []string{"ctrl-a", "ctrl-b", "ctrl-c", "ctrl-d", "ctrl-e"}
	return names[i]
}

func TestDirectionOf(t *testing.T) {
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
