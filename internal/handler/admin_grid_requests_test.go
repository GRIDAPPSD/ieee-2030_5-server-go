package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #1156: POST /api/derms/grid-requests.

const (
	gridAggA = "A000000000000000000000000000000000000001"
	gridAggB = "B000000000000000000000000000000000000001"
	gridA1   = "A100000000000000000000000000000000000001"
	gridA2   = "A200000000000000000000000000000000000001"
	gridB1   = "B100000000000000000000000000000000000001"
)

type gridFixture struct {
	dc   *dcHarness
	f    *templateFixture
	fsa  *handler.AdminFSAControlHandler
	ders *memory.ScopedStore[sep2.DER]
	caps *memory.ScopedStore[sep2.DERCapability]
	mux  *http.ServeMux
	// handler is the route's handler, so a test can change its bounds.
	handler *handler.AdminGridRequestHandler
}

// newGridFixture: aggregator A manages a1 (10 kW nameplate) and a2 (none),
// aggregator B manages b1 (5000 W nameplate).
func newGridFixture(t *testing.T) *gridFixture {
	t.Helper()
	x := newFSAControlFixture(t)
	ctx := context.Background()
	for id, lfdi := range map[string]string{"agA": gridAggA, "agB": gridAggB, "a1": gridA1, "a2": gridA2, "b1": gridB1} {
		dev := sep2.EndDevice{LFDI: lfdi, SFDI: "1"}
		dev.Href = "/edev/" + id
		if err := x.dc.devices.Create(ctx, id, dev); err != nil {
			t.Fatal(err)
		}
	}
	for agg, managed := range map[string][]string{gridAggA: {gridA1, gridA2}, gridAggB: {gridB1}} {
		for _, m := range managed {
			if err := x.dc.managers.Assign(ctx, agg, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	x.f.h.Managers = x.dc.managers
	x.f.h.FleetDevices = x.dc.devices
	g := &gridFixture{dc: x.dc, f: x.f, fsa: x.h, ders: memory.NewScopedStore[sep2.DER](), caps: memory.NewScopedStore[sep2.DERCapability]()}
	g.nameplate(t, "a1", &sep2.ActivePower{Value: 10, Multiplier: 3})
	g.nameplate(t, "b1", &sep2.ActivePower{Value: 5000})
	gh := &handler.AdminGridRequestHandler{Controls: x.h, Aggregators: x.dc.managers, DERs: g.ders, Capabilities: g.caps, MaxDeviceW: dercontrol.DefaultMaxNameplateW}
	g.handler = gh
	g.mux = http.NewServeMux()
	g.mux.HandleFunc("POST /api/derms/grid-requests", gh.HandleCreate())
	return g
}

func (g *gridFixture) nameplate(t *testing.T, edev string, w *sep2.ActivePower) {
	t.Helper()
	ctx := context.Background()
	der := sep2.DER{}
	der.Href = "/edev/" + edev + "/der/1"
	if err := g.ders.Create(ctx, edev, "1", der); err != nil {
		t.Fatal(err)
	}
	if err := g.caps.Create(ctx, edev+"/1", "default", sep2.DERCapability{RTGMaxW: w}); err != nil {
		t.Fatal(err)
	}
}

func (g *gridFixture) post(body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/derms/grid-requests", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.mux.ServeHTTP(w, req)
	return w
}

func gridRow(direction string, x, y int, start int64, duration int) string {
	s := ""
	if start != 0 {
		s = fmt.Sprintf(`,"start":%d`, start)
	}
	return fmt.Sprintf(`{"direction":%q,"x":%d,"y":%d,"durationSeconds":%d%s}`, direction, x, y, duration, s)
}

func gridBody(rows ...string) string { return `{"rows":[` + strings.Join(rows, ",") + `]}` }

func decodeGridCreated(t *testing.T, w *httptest.ResponseRecorder) handler.GridRequestCreated {
	t.Helper()
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d %s, want 201", w.Code, w.Body.String())
	}
	var got handler.GridRequestCreated
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

func decodeGridRefused(t *testing.T, w *httptest.ResponseRecorder, status int) handler.GridRequestRefused {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d %s, want %d", w.Code, w.Body.String(), status)
	}
	var got handler.GridRequestRefused
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// deviceControls is every control stored under one device, any scope.
func (g *gridFixture) deviceControls(t *testing.T, dev string) []sep2.DERControl {
	t.Helper()
	ctx := context.Background()
	parents, err := g.dc.controls.Parents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var out []sep2.DERControl
	for _, p := range parents {
		if !strings.HasPrefix(p, dev+"/") {
			continue
		}
		list, err := g.dc.controls.List(ctx, p, store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, list.Items...)
	}
	return out
}

// One row splits by nameplate: A weighs 10 kW plus a 5 kW default, B 5 kW,
// so 40 kW discharge is 30 kW to A and 10 kW to B, each one control on its
// fleet FSA carrying the share, connect and energize, on every device copy.
func TestGridRequest_SplitsByNameplateAndIssuesOnFleetFSAs(t *testing.T) {
	g := newGridFixture(t)
	got := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, 0, 900))))

	if len(got.Rows) != 1 || len(got.Rows[0].Shares) != 2 {
		t.Fatalf("reply = %+v, want one row with two shares", got)
	}
	row := got.Rows[0]
	if row.Direction != "discharge" || row.RequestedW != 40000 || row.EncodedW != 40000 || row.DifferenceW != 0 || row.DurationSeconds != 900 {
		t.Errorf("row = %+v, want discharge 40000 W encoded exactly over 900 s", row)
	}
	a, b := row.Shares[0], row.Shares[1]
	if a.AggregatorLFDI != gridAggA || a.Devices != 2 || a.WeightW != 15000 || len(a.DefaultedDevices) != 1 || a.DefaultedDevices[0] != "a2" {
		t.Errorf("share A basis = %+v, want 2 devices weighing 15000 with a2 defaulted", a)
	}
	if b.AggregatorLFDI != gridAggB || b.Devices != 1 || b.WeightW != 5000 || len(b.DefaultedDevices) != 0 {
		t.Errorf("share B basis = %+v, want 1 device weighing 5000, none defaulted", b)
	}
	if a.TargetW == nil || a.TargetW.Value != 30 || a.TargetW.Multiplier != 3 || b.TargetW == nil || b.TargetW.Value != 10 || b.TargetW.Multiplier != 3 {
		t.Fatalf("targets A %+v B %+v, want 30 x 10^3 and 10 x 10^3", a.TargetW, b.TargetW)
	}
	// 40 kW against 20 kW of nameplate also warns; the over-nameplate test pins that.
	if len(got.Warnings) != 2 || !strings.Contains(got.Warnings[0], "1 devices used the 5 kW test default") {
		t.Errorf("warnings = %q, want the test default named once, then the nameplate total", got.Warnings)
	}

	wantDevices := map[string]struct {
		share handler.GridShareView
		value int16
	}{"a1": {a, 30}, "a2": {a, 30}, "b1": {b, 10}}
	for dev, want := range wantDevices {
		ctrls := g.deviceControls(t, dev)
		if len(ctrls) != 1 {
			t.Fatalf("%s holds %d controls, want 1", dev, len(ctrls))
		}
		c := ctrls[0]
		base := c.DERControlBase
		if c.MRID != want.share.Control.MRID {
			t.Errorf("%s copy mRID %s, want the share's %s", dev, c.MRID, want.share.Control.MRID)
		}
		if base.OpModTargetW == nil || *base.OpModTargetW != (sep2.ActivePower{Value: want.value, Multiplier: 3}) {
			t.Errorf("%s opModTargetW = %+v, want %d x 10^3", dev, base.OpModTargetW, want.value)
		}
		if base.OpModConnect == nil || !*base.OpModConnect || base.OpModEnergize == nil || !*base.OpModEnergize {
			t.Errorf("%s connect %v energize %v, want both true", dev, base.OpModConnect, base.OpModEnergize)
		}
		if !strings.HasPrefix(c.Href, "/edev/"+dev+"/fsa/"+want.share.FSA+"/derp/"+want.share.Program+"/derc/") {
			t.Errorf("%s copy href %s, not under its fleet FSA %s", dev, c.Href, want.share.FSA)
		}
		if c.Interval.Duration != 900 || c.Interval.Start != row.Start {
			t.Errorf("%s interval %+v, want start %d for 900 s", dev, c.Interval, row.Start)
		}
	}
	for _, agg := range []string{"agA", "agB"} {
		if n := len(g.deviceControls(t, agg)); n != 0 {
			t.Errorf("aggregator's own EndDevice %s holds %d controls, want none", agg, n)
		}
	}
	if a.FSA == b.FSA {
		t.Errorf("both shares on fsa %s, want one fleet FSA per aggregator", a.FSA)
	}
}

// Both directions: charge is negative on the wire, and the reply says charge.
func TestGridRequest_ChargeIsNegative(t *testing.T) {
	g := newGridFixture(t)
	got := decodeGridCreated(t, g.post(gridBody(gridRow("charge", 20, 3, 0, 600))))
	row := got.Rows[0]
	if row.Direction != "charge" || row.RequestedW != -20000 || row.EncodedW != -20000 {
		t.Errorf("row = %+v, want charge -20000 encoded exactly", row)
	}
	if a := row.Shares[0]; a.TargetW == nil || a.TargetW.Value != -15 || a.TargetW.Multiplier != 3 || *a.Control.Direction != "charge" {
		t.Errorf("share A = %+v, want -15 x 10^3 labelled charge", a)
	}
	if c := g.deviceControls(t, "b1"); len(c) != 1 || c[0].DERControlBase.OpModTargetW.Value != -5 {
		t.Errorf("b1 controls = %+v, want one at -5 x 10^3", c)
	}
}

// The shares are listed by the Controls tab routes with origin derms and the
// basis kept on the record, and the ledger counts each share once.
func TestGridRequest_ControlsTabOriginAndLedgerOnce(t *testing.T) {
	g := newGridFixture(t)
	got := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, 0, 900))))
	a := got.Rows[0].Shares[0]
	ctx := context.Background()

	w := g.f.do(http.MethodGet, "/api/fsas/"+a.FSA+"/der-programs/"+a.Program+"/controls", "")
	var fsaList handler.FSAControlList
	if err := json.Unmarshal(w.Body.Bytes(), &fsaList); err != nil || w.Code != http.StatusOK {
		t.Fatalf("fsa list = %d %s", w.Code, w.Body.String())
	}
	if len(fsaList.Controls) != 1 || fsaList.Controls[0].Origin != "derms" || len(fsaList.Controls[0].Copies) != 2 {
		t.Errorf("fsa list = %+v, want one derms control with two copies", fsaList.Controls)
	}
	for _, dev := range []string{"a1", "a2"} {
		w := g.dc.do(t, http.MethodGet, "/api/der/controls?device="+dev, "")
		var l handler.DERControlList
		if err := json.Unmarshal(w.Body.Bytes(), &l); err != nil || w.Code != http.StatusOK {
			t.Fatalf("device list %s = %d %s", dev, w.Code, w.Body.String())
		}
		if len(l.Controls) != 1 || l.Controls[0].Origin != "derms" || l.Controls[0].MRID != a.Control.MRID {
			t.Errorf("device list %s = %+v, want the share with origin derms", dev, l.Controls)
		}
	}
	rec, err := g.f.h.AdminFSAs.FSAControl(ctx, a.FSA, a.Program, a.Control.ID)
	if err != nil {
		t.Fatal(err)
	}
	gs := rec.GridShare
	if gs == nil || gs.Batch != got.Batch || gs.Row != 1 || gs.AggregatorLFDI != gridAggA || gs.WeightW != 15000 || gs.TotalWeightW != 20000 ||
		gs.Units != 30 || gs.RequestValue != 40 || gs.RequestMultiplier != 3 || gs.Direction != "discharge" || len(gs.DefaultedDevices) != 1 {
		t.Errorf("stored basis = %+v, want batch %s row 1, A at 15000 of 20000 W, 30 units of 40 x 10^3", gs, got.Batch)
	}

	c, err := g.dc.h.Ledger.(*commitment.Ledger).Commitments(ctx, gridAggA, sep2time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Plain) != 1 || c.Plain[0].MRID != a.Control.MRID || c.Plain[0].TargetW == nil || c.Plain[0].TargetW.Value != 30 {
		t.Errorf("ledger plain commitments of A = %+v, want the share once at 30 x 10^3 across its 2 copies", c.Plain)
	}
}

// A chained row starts exactly at the previous row's resolved end, from a
// start of now and from a typed start alike.
func TestGridRequest_ChainedRowsStartAtThePreviousEnd(t *testing.T) {
	g := newGridFixture(t)
	start := sep2time.Now().Unix() + 7200
	follows := `{"direction":"charge","x":5,"y":3,"durationSeconds":300,"followsPrevious":true}`
	got := decodeGridCreated(t, g.post(gridBody(
		gridRow("discharge", 10, 3, 0, 600), follows,
		gridRow("discharge", 10, 3, start, 900), follows,
	)))
	r := got.Rows
	if len(r) != 4 {
		t.Fatalf("rows = %d, want 4", len(r))
	}
	if r[1].Start != r[0].Start+600 {
		t.Errorf("row 2 start %d, want row 1's end %d", r[1].Start, r[0].Start+600)
	}
	if r[2].Start != start || r[3].Start != start+900 {
		t.Errorf("rows 3 and 4 start %d and %d, want %d and %d", r[2].Start, r[3].Start, start, start+900)
	}
	if r[0].Start < sep2time.Now().Unix()-5 {
		t.Errorf("row 1 starts at %d, want now", r[0].Start)
	}
	for n, row := range r {
		for _, s := range row.Shares {
			if s.Control.Interval.Start != row.Start {
				t.Errorf("row %d share on %s starts %d, want the row's %d", n+1, s.FSA, s.Control.Interval.Start, row.Start)
			}
		}
	}
	if n := len(g.deviceControls(t, "a1")); n != 4 {
		t.Errorf("a1 holds %d controls, want 4 back to back", n)
	}
}

// Overlap is refused row against row and against live blocks, naming every
// failing row, and back to back passes. A refused batch writes nothing.
func TestGridRequest_OverlapRefusedWholeBackToBackPasses(t *testing.T) {
	g := newGridFixture(t)
	start := sep2time.Now().Unix() + 3600
	first := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600))))
	before, _ := g.dc.storedCounts(t)

	ref := decodeGridRefused(t, g.post(gridBody(
		gridRow("discharge", 10, 3, start+600, 600), // back to back with the live block: passes
		gridRow("discharge", 10, 3, start+300, 300), // inside the live block
		gridRow("discharge", 10, 3, start+900, 300), // inside row 1 of this batch
		gridRow("charge", 10, 3, start-300, 301),    // partial overlap of the live block's first second
	)), http.StatusConflict)
	if ref.Code != "rows_conflict" || len(ref.Rows) != 3 {
		t.Fatalf("refusal = %+v, want rows 2, 3 and 4", ref)
	}
	want := []handler.GridRowRefusal{
		{Row: 2, Code: "overlapping_request", ConflictsWith: &handler.GridConflict{Batch: first.Batch, Row: 1, Start: start, End: start + 600}},
		{Row: 3, Code: "overlapping_request", ConflictsWith: &handler.GridConflict{Row: 1}},
		{Row: 4, Code: "overlapping_request", ConflictsWith: &handler.GridConflict{Batch: first.Batch, Row: 1, Start: start, End: start + 600}},
	}
	for n, w := range want {
		got := ref.Rows[n]
		if got.Row != w.Row || got.Code != w.Code || got.ConflictsWith == nil || *got.ConflictsWith != *w.ConflictsWith {
			t.Errorf("refused row %d = %+v %+v, want %+v %+v", n, got, got.ConflictsWith, w, w.ConflictsWith)
		}
	}
	if after, _ := g.dc.storedCounts(t); after != before {
		t.Errorf("stored controls %d after the refused batch, want %d: the row that passed must not be written", after, before)
	}

	decodeGridCreated(t, g.post(gridBody(gridRow("charge", 10, 3, start+600, 600), gridRow("charge", 10, 3, start-600, 600))))
	if after, _ := g.dc.storedCounts(t); after != before+6 {
		t.Errorf("stored controls %d after a back to back batch, want %d", after, before+6)
	}
}

// A cancelled block no longer blocks its window.
func TestGridRequest_CancelledBlockFreesItsWindow(t *testing.T) {
	g := newGridFixture(t)
	start := sep2time.Now().Unix() + 3600
	first := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 40, 3, start, 600))))
	decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 1, 3, start, 600))), http.StatusConflict)
	for _, s := range first.Rows[0].Shares {
		if w := g.f.do(http.MethodPost, "/api/fsas/"+s.FSA+"/der-programs/"+s.Program+"/controls/"+s.Control.ID+"/cancel", `{}`); w.Code != http.StatusOK {
			t.Fatalf("cancel = %d %s", w.Code, w.Body.String())
		}
	}
	decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 1, 3, start, 600))))
}

// Rows that fail validation are all named, and nothing is read or written.
func TestGridRequest_InvalidRowsAllNamed(t *testing.T) {
	g := newGridFixture(t)
	now := sep2time.Now().Unix()
	ref := decodeGridRefused(t, g.post(gridBody(
		`{"direction":"discharge","x":1,"y":3,"durationSeconds":600,"followsPrevious":true}`,
		`{"x":1,"y":3,"durationSeconds":600}`,
		`{"direction":"discharge","x":-40,"y":3,"durationSeconds":600}`,
		`{"direction":"export","x":1,"y":3,"durationSeconds":600}`,
		gridRow("discharge", 1, 3, now-3600, 600),
		gridRow("discharge", 1, 10, 0, 600),
		gridRow("discharge", 1, 3, 0, 10),
		`{"direction":"discharge","x":1,"y":3,"durationSeconds":600,"start":`+fmt.Sprint(now+3600)+`,"followsPrevious":true}`,
		gridRow("discharge", 0, 3, 0, 600),
	)), http.StatusUnprocessableEntity)
	want := []string{"follows_previous_first_row", "direction_required", "x_negative", "direction_unknown", "start_in_past",
		"y_range", "duration_out_of_range", "start_and_follows_previous", "x_range"}
	if ref.Code != "rows_invalid" || len(ref.Rows) != len(want) {
		t.Fatalf("refusal = %+v, want %d rows", ref, len(want))
	}
	for n, code := range want {
		if ref.Rows[n].Row != n+1 || ref.Rows[n].Code != code {
			t.Errorf("refused %d = %+v, want row %d %s", n, ref.Rows[n], n+1, code)
		}
	}
	if c, _ := g.dc.storedCounts(t); c != 0 {
		t.Errorf("refused batch stored %d controls", c)
	}
	if _, ok := g.f.h.AdminFSAs.FleetFSA(gridAggA); ok {
		t.Error("a batch refused before the lock created a fleet FSA")
	}
}

// A start up to a minute in the past starts now.
func TestGridRequest_StartJustPastStartsNow(t *testing.T) {
	g := newGridFixture(t)
	now := sep2time.Now().Unix()
	got := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 1, 3, now-30, 600))))
	if got.Rows[0].Start < now {
		t.Errorf("start %d, want no earlier than now %d", got.Rows[0].Start, now)
	}
}

// A share that encodes to zero is listed and not issued.
func TestGridRequest_ZeroShareListedNotIssued(t *testing.T) {
	g := newGridFixture(t)
	got := decodeGridCreated(t, g.post(gridBody(gridRow("discharge", 1, 3, 0, 600))))
	a, b := got.Rows[0].Shares[0], got.Rows[0].Shares[1]
	if !a.Issued || a.TargetW == nil || a.TargetW.Value != 1 {
		t.Errorf("share A = %+v, want the one unit issued", a)
	}
	if b.Issued || b.TargetW != nil || b.Control != nil || b.AggregatorLFDI != gridAggB {
		t.Errorf("share B = %+v, want listed below resolution and not issued", b)
	}
	if n := len(g.deviceControls(t, "b1")); n != 0 {
		t.Errorf("b1 holds %d controls for a zero share", n)
	}
}

// With no aggregator available the batch is refused 409.
func TestGridRequest_NoAggregators(t *testing.T) {
	g := newGridFixture(t)
	ctx := context.Background()
	for _, m := range []string{gridA1, gridA2, gridB1} {
		if err := g.dc.managers.Unassign(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	ref := decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 1, 3, 0, 600))), http.StatusConflict)
	if ref.Code != "no_aggregators" {
		t.Errorf("code = %s, want no_aggregators", ref.Code)
	}
}

// A share whose fleet window holds a live grant is refused by the ledger,
// naming its row, and the other aggregator's share is not written.
func TestGridRequest_GrantedWindowRefusesTheRow(t *testing.T) {
	g := newGridFixture(t)
	ctx := context.Background()
	start := sep2time.Now().Unix() + 3600
	frp := sep2.FlowReservationResponse{EnergyAvailable: &sep2.SignedRealEnergy{Value: 1000}, PowerAvailable: &sep2.ActivePower{Value: 1000}}
	frp.Href, frp.MRID = "/edev/agA/frp/G1", "MRID-G1"
	frp.Interval = &sep2.DateTimeInterval{Start: start, Duration: 3600}
	if err := g.dc.grants.Create(ctx, "agA", "G1", frp); err != nil {
		t.Fatal(err)
	}
	ref := decodeGridRefused(t, g.post(gridBody(gridRow("discharge", 1, 3, start+7200, 600), gridRow("discharge", 40, 3, start, 600))), http.StatusConflict)
	if ref.Code != "rows_refused" || len(ref.Rows) != 1 || ref.Rows[0].Row != 2 || ref.Rows[0].Code != string(commitment.ConflictFleetWindow) {
		t.Fatalf("refusal = %+v, want row 2 %s", ref, commitment.ConflictFleetWindow)
	}
	if c, _ := g.dc.storedCounts(t); c != 0 {
		t.Errorf("refused batch stored %d controls", c)
	}
}

// Two overlapping batches raced: exactly one is written whole and the other
// refused, with nothing half-written. Each round has its own fixture, so the
// rounds' live controls never add up to the 24-event limit.
func TestGridRequest_ConcurrentOverlappingBatches(t *testing.T) {
	for round := range 20 {
		g := newGridFixture(t)
		start := sep2time.Now().Unix() + 3600
		bodies := []string{
			gridBody(gridRow("discharge", 40, 3, start, 600), gridRow("discharge", 40, 3, start+600, 600)),
			gridBody(gridRow("charge", 20, 3, start+900, 600)),
		}
		// Copies each batch writes: two rows over 3 devices, one row over 3.
		copies := []int{6, 3}
		before, _ := g.dc.storedCounts(t)
		codes := make([]int, len(bodies))
		var wg sync.WaitGroup
		for n, b := range bodies {
			wg.Add(1)
			go func() {
				defer wg.Done()
				codes[n] = g.post(b).Code
			}()
		}
		wg.Wait()
		winner := -1
		for n, c := range codes {
			if c == http.StatusCreated {
				winner = n
			}
		}
		if winner < 0 || codes[1-winner] != http.StatusConflict {
			t.Fatalf("round %d codes %v, want one 201 and one 409", round, codes)
		}
		if after, _ := g.dc.storedCounts(t); after-before != copies[winner] {
			t.Fatalf("round %d stored %d new controls, want %d from batch %d alone", round, after-before, copies[winner], winner)
		}
	}
}
