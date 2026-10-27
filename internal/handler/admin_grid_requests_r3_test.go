package handler_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// #1156 fix round 3.

// seedRetired stores one control under dev with the lifecycle lc: retired
// by it, the control would otherwise be live.
func (g *gridFixture) seedRetired(t *testing.T, dev, id string, lc dercontrol.LifecycleRecord) {
	t.Helper()
	ctx := context.Background()
	c := sep2.DERControl{}
	c.MRID = "RET-" + id
	c.Href = "/edev/" + dev + "/fsa/own/derp/p/derc/" + id
	c.CreationTime = sep2time.Now().Unix() - 100
	c.Interval = &sep2.DateTimeInterval{Start: sep2time.Now().Unix() + 200000, Duration: 600}
	if err := g.dc.controls.Create(ctx, dev+"/own/p", id, c); err != nil {
		t.Fatal(err)
	}
	if err := g.dc.lifecycles.Create(ctx, dev+"/own/p", id, lc); err != nil {
		t.Fatal(err)
	}
}

// The 24-event limit counts by the one live rule: a cancelled control and a
// superseded one do not count, so 22 live plus 2 new reaches 24 and passes.
func TestGridRequest_TooManyEventsSkipsCancelledAndSuperseded(t *testing.T) {
	g := newGridFixture(t)
	g.seedLive(t, "a1", 22)
	past := sep2time.Now().Unix() - 50
	g.seedRetired(t, "a1", "cancelled", dercontrol.LifecycleRecord{CancelledAt: &past})
	g.seedRetired(t, "a1", "superseded", dercontrol.LifecycleRecord{SupersededAt: &past, SupersededBy: "NEWER"})
	start := sep2time.Now().Unix() + 3600
	decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600), gridRow("discharge", 40, 3, start+600, 600))))
}

// Control: the same two controls left live do count, so the same batch is
// refused.
func TestGridRequest_TooManyEventsCountsTheSameControlsLive(t *testing.T) {
	g := newGridFixture(t)
	g.seedLive(t, "a1", 22)
	g.seedRetired(t, "a1", "live1", dercontrol.LifecycleRecord{})
	g.seedRetired(t, "a1", "live2", dercontrol.LifecycleRecord{})
	start := sep2time.Now().Unix() + 3600
	ref := decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600), gridRow("discharge", 40, 3, start+600, 600))), http.StatusConflict)
	if len(ref.Devices) != 1 || ref.Devices[0].Live != 24 {
		t.Errorf("devices = %+v, want a1 at 24 live", ref.Devices)
	}
}

// A device exactly at the nameplate cap keeps its nameplate; one watt over
// takes the default.
func TestGridRequest_NameplateCapBoundary(t *testing.T) {
	for _, tc := range []struct {
		name     string
		w        sep2.ActivePower
		weight   float64
		overCaps int
	}{
		{"at the cap", sep2.ActivePower{Value: 1000, Multiplier: 3}, 1000000, 0},
		{"one watt over", sep2.ActivePower{Value: 1000, Multiplier: 3}, 5000, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGridFixture(t)
			w := tc.w
			if tc.overCaps == 1 {
				// 1000001 W does not fit an int16 value exactly, so one watt
				// over is the same 1 MW against a cap one watt lower.
				g.handler.MaxDeviceW = 999999
			}
			if err := g.caps.Update(context.Background(), "b1/1", "default", sep2.DERCapability{RTGMaxW: &w}); err != nil {
				t.Fatal(err)
			}
			got := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, 0, 600))))
			b := got.Rows[0].Shares[1]
			if b.WeightW != tc.weight || len(b.OverCapDevices) != tc.overCaps {
				t.Errorf("B = weight %g over cap %v, want %g and %d", b.WeightW, b.OverCapDevices, tc.weight, tc.overCaps)
			}
		})
	}
}
