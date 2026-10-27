package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1156 fix round 1.

const gridA3 = "A300000000000000000000000000000000000001"

// addManaged adds device id under aggregator A after the fixture is built.
func (g *gridFixture) addManaged(t *testing.T, id, lfdi string) {
	t.Helper()
	ctx := context.Background()
	dev := sep2.EndDevice{LFDI: lfdi, SFDI: "1"}
	dev.Href = "/edev/" + id
	if err := g.dc.devices.Create(ctx, id, dev); err != nil {
		t.Fatal(err)
	}
	if err := g.dc.managers.Assign(ctx, gridAggA, lfdi); err != nil {
		t.Fatal(err)
	}
}

func (g *gridFixture) fleetDevices(t *testing.T, agg string) []string {
	t.Helper()
	id, ok := g.f.h.AdminFSAs.FleetFSA(agg)
	if !ok {
		t.Fatalf("no fleet FSA for %s", agg)
	}
	return g.f.h.AdminFSAs.Devices(context.Background(), id)
}

// failingControls fails every copy create under one device and, as it does,
// makes the FSA snapshot directory read-only, so the record undo fails too.
type failingControls struct {
	*memory.DERControlStore
	device string
	dir    string
}

func (c *failingControls) Create(ctx context.Context, parentID, id string, ctrl sep2.DERControl) error {
	if strings.HasPrefix(parentID, c.device+"/") {
		if err := os.Chmod(c.dir, 0o500); err != nil {
			return err
		}
		return errors.New("disk full")
	}
	return c.DERControlStore.Create(ctx, parentID, id, ctrl)
}

// Item 1: a later share whose copy and record undo both fail leaves no
// earlier share on any device, and the reply names every control kept with
// the batch, never saying no device holds one while one does.
func TestGridRequest_UndoFailureUndoesEveryShareAndNamesTheKept(t *testing.T) {
	g := newGridFixture(t)
	dir := t.TempDir()
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	admin, err := memory.NewAdminFSAStoreWithPersistence(filepath.Join(dir, "fsas.json"))
	if err != nil {
		t.Fatal(err)
	}
	g.f.h.AdminFSAs = admin
	g.dc.h.FSAControls = admin
	pen := dcPEN
	issuer, err := dercontrol.NewIssuer(g.dc.programs, &failingControls{DERControlStore: g.dc.controls, device: "b1", dir: dir}, g.dc.lifecycles, dercontrol.Config{PEN: &pen})
	if err != nil {
		t.Fatal(err)
	}
	g.fsa.Issuer, g.dc.h.Issuer, g.f.h.Retirer = issuer, issuer, issuer

	w := g.post(gridBody(gridRow("discharge", 40, 3, 0, 900)))
	for _, dev := range []string{"a1", "a2", "b1"} {
		if n := len(g.deviceControls(t, dev)); n != 0 {
			t.Errorf("%s holds %d controls after the failed batch, want 0", dev, n)
		}
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d %s, want 500", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "no device holds") {
		t.Errorf("reply says no device holds the control: %s", w.Body.String())
	}
	var got struct {
		Batch string `json:"batch"`
		Kept  []struct {
			Row            int    `json:"row"`
			AggregatorLFDI string `json:"aggregatorLFDI"`
			MRID           string `json:"mRID"`
			OnDevices      bool   `json:"onDevices"`
			CancelHref     string `json:"cancelHref"`
		} `json:"kept"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Batch == "" || len(got.Kept) != 2 {
		t.Fatalf("reply = %s, want the batch and both shares' records named", w.Body.String())
	}
	for n, agg := range []string{gridAggA, gridAggB} {
		k := got.Kept[n]
		if k.AggregatorLFDI != agg || k.Row != 1 || k.MRID == "" || k.OnDevices || !strings.HasSuffix(k.CancelHref, "/cancel") {
			t.Errorf("kept %d = %+v, want %s's record, on no device, with its cancel href", n, k, agg)
		}
	}
	if !strings.Contains(g.dc.logs.String(), "der_grid_request_incomplete") || strings.Count(g.dc.logs.String(), "der_grid_request_incomplete") != 1 {
		t.Errorf("want one der_grid_request_incomplete log line, logs:\n%s", g.dc.logs.String())
	}
}

// Item 2: a row the ledger refuses is named beside a row the overlap check
// refuses.
func TestGridRequest_OverlapAndLedgerRefusalsBothNamed(t *testing.T) {
	g := newGridFixture(t)
	ctx := context.Background()
	start := sep2time.Now().Unix() + 3600
	decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600))))
	granted := start + 7200
	frp := sep2.FlowReservationResponse{EnergyAvailable: &sep2.SignedRealEnergy{Value: 1000}, PowerAvailable: &sep2.ActivePower{Value: 1000}}
	frp.Href, frp.MRID = "/edev/agA/frp/G1", "MRID-G1"
	frp.Interval = &sep2.DateTimeInterval{Start: granted, Duration: 3600}
	if err := g.dc.grants.Create(ctx, "agA", "G1", frp); err != nil {
		t.Fatal(err)
	}
	ref := decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 40, 3, granted, 600), gridRow("discharge", 40, 3, start+300, 600))), http.StatusConflict)
	codes := map[int]string{}
	for _, r := range ref.Rows {
		codes[r.Row] = r.Code
	}
	if codes[1] != string(commitment.ConflictFleetWindow) || codes[2] != "overlapping_request" || len(ref.Rows) != 2 {
		t.Errorf("refused rows = %+v, want row 1 %s and row 2 overlapping_request", ref.Rows, commitment.ConflictFleetWindow)
	}
}

// Item 3: a refused batch assigns no newly managed device.
func TestGridRequest_RefusedBatchDoesNotReconcile(t *testing.T) {
	g := newGridFixture(t)
	start := sep2time.Now().Unix() + 3600
	decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600))))
	g.addManaged(t, "a3", gridA3)
	decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 40, 3, start+300, 600))), http.StatusConflict)
	if got := g.fleetDevices(t, gridAggA); slices.Contains(got, "a3") {
		t.Errorf("fleet A devices %v after a refused batch, want a3 not assigned", got)
	}
	// The next batch that passes reaches a3.
	decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, start+600, 600))))
	if got := g.fleetDevices(t, gridAggA); !slices.Contains(got, "a3") {
		t.Errorf("fleet A devices %v after an accepted batch, want a3", got)
	}
	// a3 got the earlier share by replication and this one written to it:
	// both copies carry the fleet total mark the ledger counts once by.
	ctrls := g.deviceControls(t, "a3")
	if len(ctrls) != 2 {
		t.Fatalf("a3 holds %d controls, want the replicated share and the new one", len(ctrls))
	}
	for _, c := range ctrls {
		id := c.Href[strings.LastIndex(c.Href, "/")+1:]
		scope := strings.TrimPrefix(c.Href[:strings.Index(c.Href, "/derc/")], "/edev/")
		scope = strings.Replace(strings.Replace(scope, "/fsa/", "/", 1), "/derp/", "/", 1)
		lc, err := g.dc.lifecycles.Get(context.Background(), scope, id)
		if err != nil || !lc.FleetTotal || lc.Origin != dercontrol.OriginDERMS {
			t.Errorf("a3 copy %s lifecycle = %+v, %v; want derms and a fleet total", c.MRID, lc, err)
		}
	}
}

// Item 4: one device that cannot be assigned refuses the batch with a 409
// naming its aggregator and the device, and writes nothing.
func TestGridRequest_UnassignableDeviceIsNamed(t *testing.T) {
	g := newGridFixture(t)
	ctx := context.Background()
	first := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, sep2time.Now().Unix()+3600, 600))))
	g.addManaged(t, "a3", gridA3)
	tmpl := first.Rows[0].Shares[0].Program
	clash := sep2.DERProgram{MRID: "CLASH", Description: "device's own"}
	clash.Href = "/edev/a3/fsa/own/derp/" + tmpl
	if err := g.dc.programs.Create(ctx, "a3", tmpl, clash); err != nil {
		t.Fatal(err)
	}
	before, _ := g.dc.storedCounts(t)
	ref := decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 40, 3, sep2time.Now().Unix()+7200, 600))), http.StatusConflict)
	if ref.Code != "aggregator_unavailable" || len(ref.Aggregators) != 1 || ref.Aggregators[0].AggregatorLFDI != gridAggA || ref.Aggregators[0].Device != "a3" {
		t.Fatalf("refusal = %+v, want aggregator_unavailable naming %s and a3", ref, gridAggA)
	}
	if len(ref.Rows) != 1 || ref.Rows[0].Row != 1 || ref.Rows[0].AggregatorLFDI != gridAggA {
		t.Errorf("rows = %+v, want row 1 refused on %s", ref.Rows, gridAggA)
	}
	if after, _ := g.dc.storedCounts(t); after != before {
		t.Errorf("stored controls %d after the refusal, want %d", after, before)
	}
}

// Item 5: a device whose nameplate is over the cap is weighted at the test
// default and named, so it cannot take the whole request.
func TestGridRequest_NameplateOverCapIgnored(t *testing.T) {
	g := newGridFixture(t)
	ctx := context.Background()
	if err := g.caps.Update(ctx, "b1/1", "default", sep2.DERCapability{RTGMaxW: &sep2.ActivePower{Value: 32767, Multiplier: 9}}); err != nil {
		t.Fatal(err)
	}
	got := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, 0, 900))))
	a, b := got.Rows[0].Shares[0], got.Rows[0].Shares[1]
	if a.TargetW == nil || a.TargetW.Value != 30 || b.TargetW == nil || b.TargetW.Value != 10 || b.WeightW != 5000 {
		t.Errorf("shares A %+v B %+v, want 30 and 10 x 10^3 with B weighted 5000", a.TargetW, b)
	}
	if len(b.OverCapDevices) != 1 || b.OverCapDevices[0] != "b1" {
		t.Errorf("B over cap = %v, want b1", b.OverCapDevices)
	}
	if !slices.ContainsFunc(got.Warnings, func(s string) bool { return strings.Contains(s, "nameplate ignored, over cap") }) {
		t.Errorf("warnings = %q, want the over-cap device named", got.Warnings)
	}
}

// Item 4: a device whose assignment fails when its fleet is reconciled, after
// every check passed, refuses the batch with a 409 naming it, and no share
// is written.
func TestGridRequest_ReconcileFailureIsNamed(t *testing.T) {
	g := newGridFixture(t)
	ctx := context.Background()
	start := sep2time.Now().Unix() + 3600
	decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600))))
	// A grant stored over the live share: replicating that share to a newly
	// assigned device is refused by the ledger.
	frp := sep2.FlowReservationResponse{EnergyAvailable: &sep2.SignedRealEnergy{Value: 1000}, PowerAvailable: &sep2.ActivePower{Value: 1000}}
	frp.Href, frp.MRID = "/edev/agA/frp/G1", "MRID-G1"
	frp.Interval = &sep2.DateTimeInterval{Start: start, Duration: 600}
	if err := g.dc.grants.Create(ctx, "agA", "G1", frp); err != nil {
		t.Fatal(err)
	}
	g.addManaged(t, "a3", gridA3)
	before, _ := g.dc.storedCounts(t)
	ref := decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 40, 3, start+7200, 600))), http.StatusConflict)
	if ref.Code != "aggregator_unavailable" || len(ref.Aggregators) != 1 || ref.Aggregators[0].Device != "a3" || ref.Aggregators[0].Code != "device_not_assignable" {
		t.Fatalf("refusal = %+v, want a3 named device_not_assignable", ref)
	}
	if after, _ := g.dc.storedCounts(t); after != before {
		t.Errorf("stored controls %d after the refusal, want %d", after, before)
	}
}
