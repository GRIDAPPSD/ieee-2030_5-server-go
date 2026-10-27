package handler_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// #1156 fix round 2.

// A row that rounds reports encoded minus requested, signed: charge
// 100001 W over 15 kW and 5 kW is 75001 and 25000 units, encoded 7500 x 10
// and 25000 x 1, so -100000 W against -100001, a difference of +1.
func TestGridRequest_RoundedRowDifference(t *testing.T) {
	g := newGridFixture(t)
	got := decodeGridCreated(t, g.post(gridBody(gridRow("charge", 100001, 0, 0, 600))))
	row := got.Rows[0]
	if row.RequestedW != -100001 || row.EncodedW != -100000 || row.DifferenceW != 1 {
		t.Errorf("row = requested %g encoded %g difference %g, want -100001, -100000, +1", row.RequestedW, row.EncodedW, row.DifferenceW)
	}
	a, b := row.Shares[0].TargetW, row.Shares[1].TargetW
	if a == nil || a.Value != -7500 || a.Multiplier != 1 || b == nil || b.Value != -25000 || b.Multiplier != 0 {
		t.Errorf("targets %+v %+v, want -7500 x 10 and -25000 x 1", a, b)
	}
	if !slices.ContainsFunc(got.Warnings, func(s string) bool { return strings.Contains(s, "row 1 asks for 100001 W, more than the 20000 W") }) {
		t.Errorf("warnings = %q, want row 1 named over the fleets' nameplate", got.Warnings)
	}
}

// seedLive stores n live controls under a device, outside any fleet, as a
// device program's own would be.
func (g *gridFixture) seedLive(t *testing.T, dev string, n int) {
	t.Helper()
	ctx := context.Background()
	start := sep2time.Now().Unix() + 100000
	for i := range n {
		id := fmt.Sprintf("own%02d", i)
		c := sep2.DERControl{}
		c.MRID = fmt.Sprintf("OWN%02d", i)
		c.Href = "/edev/" + dev + "/fsa/own/derp/p/derc/" + id
		c.Interval = &sep2.DateTimeInterval{Start: start + int64(i)*600, Duration: 600}
		if err := g.dc.controls.Create(ctx, dev+"/own/p", id, c); err != nil {
			t.Fatal(err)
		}
	}
}

// A batch that would take a device past 24 live events is refused naming
// the device and the rows; reaching 24 exactly passes.
func TestGridRequest_TooManyEvents(t *testing.T) {
	g := newGridFixture(t)
	g.seedLive(t, "a1", 23)
	start := sep2time.Now().Unix() + 3600
	before, _ := g.dc.storedCounts(t)
	ref := decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600), gridRow("discharge", 40, 3, start+600, 600))), http.StatusConflict)
	if ref.Code != "too_many_events" || len(ref.Devices) != 1 || ref.Devices[0].Device != "a1" || ref.Devices[0].Live != 23 || ref.Devices[0].Added != 2 {
		t.Fatalf("refusal = %+v, want too_many_events naming a1 at 23 live and 2 added", ref)
	}
	if len(ref.Rows) != 2 || ref.Rows[0].Row != 1 || ref.Rows[1].Row != 2 || ref.Rows[0].Code != "too_many_events" || ref.Rows[0].AggregatorLFDI != gridAggA {
		t.Errorf("rows = %+v, want rows 1 and 2 refused on %s", ref.Rows, gridAggA)
	}
	if after, _ := g.dc.storedCounts(t); after != before {
		t.Errorf("stored %d controls after the refusal, want %d", after, before)
	}
	decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600))))
}

// A cancelled or ended control does not count toward the 24.
func TestGridRequest_TooManyEventsCountsOnlyLive(t *testing.T) {
	g := newGridFixture(t)
	g.seedLive(t, "a1", 22)
	ctx := context.Background()
	ended := sep2.DERControl{}
	ended.MRID, ended.Href = "ENDED", "/edev/a1/fsa/own/derp/p/derc/ended"
	ended.Interval = &sep2.DateTimeInterval{Start: sep2time.Now().Unix() - 7200, Duration: 600}
	if err := g.dc.controls.Create(ctx, "a1/own/p", "ended", ended); err != nil {
		t.Fatal(err)
	}
	start := sep2time.Now().Unix() + 3600
	decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600), gridRow("discharge", 40, 3, start+600, 600))))
}

// A row the issuer refuses while resolving its start is named, and the
// check still runs over the others.
func TestGridRequest_StartTooFarAheadNamed(t *testing.T) {
	g := newGridFixture(t)
	follows := `{"direction":"discharge","x":1,"y":3,"durationSeconds":86400,"followsPrevious":true}`
	rows := []string{gridRow("discharge", 1, 3, 0, 86400)}
	for range 8 {
		rows = append(rows, follows)
	}
	ref := decodeGridRefused(t, g.post(gridBody(rows...)), http.StatusUnprocessableEntity)
	if len(ref.Rows) != 1 || ref.Rows[0].Row != 9 || ref.Rows[0].Code != "start_too_far_ahead" {
		t.Errorf("refusal = %+v, want row 9 start_too_far_ahead alone", ref)
	}
}

// A row chained to a refused row is refused as follows_invalid.
func TestGridRequest_FollowsInvalid(t *testing.T) {
	g := newGridFixture(t)
	ref := decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 0, 3, 0, 600),
		`{"direction":"discharge","x":1,"y":3,"durationSeconds":600,"followsPrevious":true}`)), http.StatusUnprocessableEntity)
	if len(ref.Rows) != 2 || ref.Rows[0].Code != "x_range" || ref.Rows[1].Code != "follows_invalid" {
		t.Errorf("rows = %+v, want x_range then follows_invalid", ref.Rows)
	}
}

// Aggregators that take no share are named in the warnings.
func TestGridRequest_SkippedAggregatorsWarned(t *testing.T) {
	g := newGridFixture(t)
	ctx := context.Background()
	const aggC, aggD = "C000000000000000000000000000000000000001", "D000000000000000000000000000000000000001"
	dev := sep2.EndDevice{LFDI: aggC, SFDI: "1"}
	dev.Href = "/edev/agC"
	if err := g.dc.devices.Create(ctx, "agC", dev); err != nil {
		t.Fatal(err)
	}
	for agg, managed := range map[string]string{aggC: "C100000000000000000000000000000000000001", aggD: "D100000000000000000000000000000000000001"} {
		if err := g.dc.managers.Assign(ctx, agg, managed); err != nil {
			t.Fatal(err)
		}
	}
	got := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 10, 3, 0, 600))))
	want := []string{
		"aggregator " + aggC + " skipped: it manages no device with an EndDevice record",
		"aggregator " + aggD + " skipped: it has no EndDevice record",
	}
	for _, w := range want {
		if !slices.Contains(got.Warnings, w) {
			t.Errorf("warnings = %q, want %q", got.Warnings, w)
		}
	}
	if len(got.Rows[0].Shares) != 2 {
		t.Errorf("shares = %d, want A and B only", len(got.Rows[0].Shares))
	}
}

// A share that fits no multiplier is refused as share_out_of_range.
func TestGridRequest_ShareOutOfRange(t *testing.T) {
	g := newGridFixture(t)
	ref := decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 999999999, 9, 0, 600))), http.StatusUnprocessableEntity)
	if len(ref.Rows) != 1 || ref.Rows[0].Code != "share_out_of_range" {
		t.Errorf("refusal = %+v, want row 1 share_out_of_range", ref)
	}
	if c, _ := g.dc.storedCounts(t); c != 0 {
		t.Errorf("stored %d controls", c)
	}
}

// A zero rtgMaxW counts as no nameplate, and a device's DERs are summed.
func TestGridRequest_ZeroNameplateAndSummedDERs(t *testing.T) {
	g := newGridFixture(t)
	ctx := context.Background()
	der := sep2.DER{}
	der.Href = "/edev/a1/der/2"
	if err := g.ders.Create(ctx, "a1", "2", der); err != nil {
		t.Fatal(err)
	}
	if err := g.caps.Create(ctx, "a1/2", "default", sep2.DERCapability{RTGMaxW: &sep2.ActivePower{Value: 5, Multiplier: 3}}); err != nil {
		t.Fatal(err)
	}
	g.nameplate(t, "a2", &sep2.ActivePower{Value: 0})
	got := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, 0, 600))))
	a, b := got.Rows[0].Shares[0], got.Rows[0].Shares[1]
	if a.WeightW != 20000 || len(a.DefaultedDevices) != 1 || a.DefaultedDevices[0] != "a2" {
		t.Errorf("A = weight %g defaulted %v, want 15000 from a1's two DERs plus 5000 for a2 at zero", a.WeightW, a.DefaultedDevices)
	}
	if a.TargetW == nil || a.TargetW.Value != 32 || b.TargetW == nil || b.TargetW.Value != 8 {
		t.Errorf("targets %+v %+v, want 32 and 8 x 10^3", a.TargetW, b.TargetW)
	}
}

// A row refused on both aggregators is named once.
func TestGridRequest_RowRefusedOnTwoFleetsNamedOnce(t *testing.T) {
	g := newGridFixture(t)
	ctx := context.Background()
	start := sep2time.Now().Unix() + 3600
	for _, agg := range []string{"agA", "agB"} {
		frp := sep2.FlowReservationResponse{EnergyAvailable: &sep2.SignedRealEnergy{Value: 1000}, PowerAvailable: &sep2.ActivePower{Value: 1000}}
		frp.Href, frp.MRID = "/edev/"+agg+"/frp/G1", "MRID-G1-"+agg
		frp.Interval = &sep2.DateTimeInterval{Start: start, Duration: 3600}
		if err := g.dc.grants.Create(ctx, agg, "G1", frp); err != nil {
			t.Fatal(err)
		}
	}
	ref := decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600))), http.StatusConflict)
	if len(ref.Rows) != 1 || ref.Rows[0].Row != 1 || ref.Rows[0].Code != string(commitment.ConflictFleetWindow) {
		t.Errorf("rows = %+v, want row 1 once", ref.Rows)
	}
}

// The success line names the batch, the watts per row and the mRIDs.
func TestGridRequest_SuccessLogNamesTheBatch(t *testing.T) {
	g := newGridFixture(t)
	got := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, 0, 600))))
	logs := g.dc.logs.String()
	for _, want := range []string{"event=der_grid_request_created", "batch=" + got.Batch, "requested_w=[40000]", "encoded_w=[40000]", got.Rows[0].Shares[0].Control.MRID} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %q:\n%s", want, logs)
		}
	}
}
