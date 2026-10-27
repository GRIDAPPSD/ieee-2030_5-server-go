package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const ceD3 = "BBBB000000000000000000000000000000000003"

// ceHarness is two devices managed by powerAgg (d1, d3) and one no aggregator
// manages (d2), over real in-memory stores, with the clock pinned to powerNow.
type ceHarness struct {
	t     *testing.T
	ctrls *memory.DERControlStore
	lcs   *dercontrol.LifecycleStore
	fsas  *memory.AdminFSAStore
	mgr   *memory.EndDeviceManagementStore
	devs  *memory.EndDeviceStore
}

func newCEHarness(t *testing.T) *ceHarness {
	t.Helper()
	old := nowUnix
	nowUnix = func() int64 { return powerNow }
	t.Cleanup(func() { nowUnix = old })
	h := &ceHarness{
		t: t, ctrls: memory.NewDERControlStore(), lcs: dercontrol.NewLifecycleStore(),
		fsas: memory.NewAdminFSAStore(), mgr: memory.NewEndDeviceManagementStore(), devs: memory.NewEndDeviceStore(),
	}
	ctx := context.Background()
	for id, lfdi := range map[string]string{"d1": powerD1, "d2": powerD2, "d3": ceD3} {
		if err := h.devs.Create(ctx, id, sep2.EndDevice{LFDI: lfdi, SFDI: id}); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []string{powerD1, ceD3} {
		if err := h.mgr.Assign(ctx, powerAgg, m); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func ceTarget(w int16) *sep2.DERControlBase {
	return &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: w}}
}

// issue stores a control under scope with its lifecycle record.
func (h *ceHarness) issue(scope dercontrol.Scope, id, mrid string, start int64, dur uint32, base *sep2.DERControlBase, lc dercontrol.LifecycleRecord) {
	h.t.Helper()
	c := sep2.DERControl{DERControlBase: base}
	c.Interval = &sep2.DateTimeInterval{Start: start, Duration: dur}
	c.MRID, c.Href, c.CreationTime = mrid, scope.ControlHref(id), start-60
	ctx := context.Background()
	if err := h.ctrls.Create(ctx, scope.Key(), id, c); err != nil {
		h.t.Fatal(err)
	}
	if err := h.lcs.Create(ctx, scope.Key(), id, lc); err != nil {
		h.t.Fatal(err)
	}
}

func (h *ceHarness) get(query string) (int, ControlEventsResponse) {
	h.t.Helper()
	hd := &AdminControlEventsHandler{Controls: h.ctrls, Lifecycles: h.lcs, EndDevices: h.devs, FSAs: h.fsas, Fleet: &AdminFleetHandler{Managers: h.mgr}}
	w := httptest.NewRecorder()
	hd.HandleControlEvents()(w, httptest.NewRequest(http.MethodGet, "/api/derms/control-events?"+query, nil))
	var out ControlEventsResponse
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			h.t.Fatal(err)
		}
	}
	return w.Code, out
}

func ceScope(dev string) dercontrol.Scope {
	return dercontrol.Scope{EndDeviceID: dev, FSAID: "0", DERProgramID: "0"}
}

func TestControlEvents_WindowFilterAndDirectGroup(t *testing.T) {
	h := newCEHarness(t)
	now := powerNow
	h.issue(ceScope("d2"), "before", "MR-BEFORE", now-7200, 600, ceTarget(1000), dercontrol.LifecycleRecord{})
	h.issue(ceScope("d2"), "inside", "MR-INSIDE", now-1800, 600, ceTarget(-2000), dercontrol.LifecycleRecord{})
	h.issue(ceScope("d2"), "straddle", "MR-STRADDLE", now-4000, 1000, ceTarget(500), dercontrol.LifecycleRecord{})
	h.issue(ceScope("d2"), "after", "MR-AFTER", now+7200, 600, ceTarget(1000), dercontrol.LifecycleRecord{})

	code, got := h.get("window=3600")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if got.WindowEnd != now || got.WindowStart != now-3600 {
		t.Errorf("window = [%d, %d], want [%d, %d]", got.WindowStart, got.WindowEnd, now-3600, now)
	}
	if len(got.Events) != 2 || got.Events[0].MRID != "MR-STRADDLE" || got.Events[1].MRID != "MR-INSIDE" {
		t.Fatalf("events = %+v, want MR-STRADDLE then MR-INSIDE only", got.Events)
	}
	e := got.Events[1]
	if e.Group != "direct" || e.GroupKind != "direct" || e.Scope != "device" || e.FSAID != "" {
		t.Errorf("group/kind/scope/fsa = %q/%q/%q/%q, want direct/direct/device/empty", e.Group, e.GroupKind, e.Scope, e.FSAID)
	}
	if e.TargetW == nil || *e.TargetW != -2000 || e.Direction == nil || *e.Direction != "charge" {
		t.Errorf("target/direction = %v/%v, want -2000/charge", e.TargetW, e.Direction)
	}
	if e.Start != now-1800 || e.End != now-1200 || e.Status != "completed" || e.Origin != "admin" {
		t.Errorf("start/end/status/origin = %d/%d/%s/%s, want %d/%d/completed/admin", e.Start, e.End, e.Status, e.Origin, now-1800, now-1200)
	}
	if len(e.DeviceIDs) != 1 || e.DeviceIDs[0] != "d2" || len(e.Modes) != 1 || e.Modes[0] != "opModTargetW" {
		t.Errorf("devices/modes = %v/%v, want [d2]/[opModTargetW]", e.DeviceIDs, e.Modes)
	}
}

func TestControlEvents_CancelledControlClippedToInForceTime(t *testing.T) {
	h := newCEHarness(t)
	now := powerNow
	cancelled := now - 1200
	h.issue(ceScope("d1"), "c1", "MR-CANCEL", now-1800, 1800, ceTarget(3000), dercontrol.LifecycleRecord{CancelledAt: &cancelled})
	never := now - 2000
	h.issue(ceScope("d1"), "c2", "MR-NEVER", now-1500, 600, ceTarget(3000), dercontrol.LifecycleRecord{CancelledAt: &never})
	superseded := now - 900
	h.issue(ceScope("d1"), "c3", "MR-SUPER", now-1700, 1700, ceTarget(3000), dercontrol.LifecycleRecord{SupersededAt: &superseded})

	_, got := h.get("window=3600")
	if len(got.Events) != 2 {
		t.Fatalf("events = %+v, want the cancelled and superseded controls only", got.Events)
	}
	c, s := got.Events[0], got.Events[1]
	if c.MRID != "MR-CANCEL" || c.End != cancelled || c.Status != "cancelled" {
		t.Errorf("cancelled event = %s end %d %s, want MR-CANCEL end %d cancelled", c.MRID, c.End, c.Status, cancelled)
	}
	if s.MRID != "MR-SUPER" || s.End != superseded || s.Status != "superseded" {
		t.Errorf("superseded event = %s end %d %s, want MR-SUPER end %d superseded", s.MRID, s.End, s.Status, superseded)
	}
	if c.Group != canonicalLFDI(powerAgg) || c.GroupKind != "aggregator" {
		t.Errorf("group = %q kind %q, want the aggregator %q", c.Group, c.GroupKind, canonicalLFDI(powerAgg))
	}
}

// A control sent to a fleet FSA is stored once per device, and appears once.
func TestControlEvents_FleetControlOncePerFleetWithDERMSOrigin(t *testing.T) {
	h := newCEHarness(t)
	ctx := context.Background()
	now := powerNow
	if err := h.fsas.CreateFleet(ctx, "fsaF", sep2.FunctionSetAssignments{}, powerAgg); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"d1", "d3"} {
		if err := h.fsas.AssignDevice(ctx, "fsaF", d); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.fsas.CreateTemplate(ctx, "fsaF", memory.ProgramTemplate{ID: "t1", MRID: "TMPL1"}); err != nil {
		t.Fatal(err)
	}
	rec := memory.FSAControl{
		TemplateID: "t1", ID: "c1", MRID: "MR-FLEET", CreationTime: now - 700, Start: now - 600, Duration: 1200,
		Base: *ceTarget(5000), Origin: dercontrol.OriginDERMS,
	}
	if err := h.fsas.CreateFSAControl(ctx, "fsaF", rec); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"d1", "d3"} {
		h.issue(dercontrol.Scope{EndDeviceID: d, FSAID: "fsaF", DERProgramID: "t1"}, "c1", "MR-FLEET", now-600, 1200, ceTarget(5000), dercontrol.LifecycleRecord{Origin: dercontrol.OriginDERMS})
	}

	code, got := h.get("window=900")
	if code != http.StatusOK || len(got.Events) != 1 {
		t.Fatalf("status %d events %+v, want one event for two device copies", code, got.Events)
	}
	e := got.Events[0]
	if e.Scope != "fsa" || e.FSAID != "fsaF" || e.Group != canonicalLFDI(powerAgg) || e.GroupKind != "aggregator" {
		t.Errorf("scope/fsa/group/kind = %s/%s/%s/%s", e.Scope, e.FSAID, e.Group, e.GroupKind)
	}
	if len(e.DeviceIDs) != 2 || e.DeviceIDs[0] != "d1" || e.DeviceIDs[1] != "d3" {
		t.Errorf("devices = %v, want [d1 d3]", e.DeviceIDs)
	}
	if e.Origin != "derms" || e.Direction == nil || *e.Direction != "discharge" || e.TargetW == nil || *e.TargetW != 5000 {
		t.Errorf("origin/direction/target = %s/%v/%v, want derms/discharge/5000", e.Origin, e.Direction, e.TargetW)
	}
	if e.Status != "active" || e.Start != now-600 || e.End != now+600 {
		t.Errorf("status/start/end = %s/%d/%d, want active/%d/%d", e.Status, e.Start, e.End, now-600, now+600)
	}

	// A record cancelled by the operator reads cancelled and ends at the cancel.
	at := now - 300
	rec.CancelledAt = &at
	if err := h.fsas.UpdateFSAControl(ctx, "fsaF", rec); err != nil {
		t.Fatal(err)
	}
	_, got = h.get("window=900")
	if len(got.Events) != 1 || got.Events[0].Status != "cancelled" || got.Events[0].End != at {
		t.Errorf("after cancel events = %+v, want one cancelled event ending %d", got.Events, at)
	}
}

func TestControlEvents_ScopeFilterAndRefusals(t *testing.T) {
	h := newCEHarness(t)
	now := powerNow
	h.issue(ceScope("d1"), "a", "MR-AGG", now-600, 600, ceTarget(100), dercontrol.LifecycleRecord{})
	h.issue(ceScope("d2"), "b", "MR-DIRECT", now-600, 600, ceTarget(100), dercontrol.LifecycleRecord{})

	_, all := h.get("window=900&scope=all")
	if len(all.Events) != 2 {
		t.Fatalf("scope=all events = %+v, want 2", all.Events)
	}
	_, one := h.get("window=900&scope=" + powerAgg)
	if len(one.Events) != 1 || one.Events[0].MRID != "MR-AGG" {
		t.Errorf("aggregator scope events = %+v, want MR-AGG only", one.Events)
	}
	if code, _ := h.get("window=900&scope=BBBB000000000000000000000000000000000009"); code != http.StatusNotFound {
		t.Errorf("unknown aggregator = %d, want 404", code)
	}
	if code, _ := h.get("window=900&scope=zz"); code != http.StatusBadRequest {
		t.Errorf("malformed scope = %d, want 400", code)
	}
	if code, _ := h.get("window=123"); code != http.StatusBadRequest {
		t.Errorf("bad window = %d, want 400", code)
	}
}

func TestControlEvents_EmptyEventsIsArray(t *testing.T) {
	h := newCEHarness(t)
	hd := &AdminControlEventsHandler{Controls: h.ctrls, Lifecycles: h.lcs, EndDevices: h.devs, FSAs: h.fsas, Fleet: &AdminFleetHandler{Managers: h.mgr}}
	w := httptest.NewRecorder()
	hd.HandleControlEvents()(w, httptest.NewRequest(http.MethodGet, "/api/derms/control-events?window=900", nil))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["events"]) != "[]" {
		t.Errorf("events = %s, want []", raw["events"])
	}
}

// The lanes are the power route's groups: the same keys in the same order,
// aggregators sorted and Direct last, whatever order the events arrive in.
func TestControlEvents_LanesEqualPowerGroups(t *testing.T) {
	h := newCEHarness(t)
	ctx := context.Background()
	const aggB, d4 = "CCCC000000000000000000000000000000000001", "DDDD000000000000000000000000000000000004"
	if err := h.mgr.Assign(ctx, aggB, powerD2); err != nil {
		t.Fatal(err)
	}
	if err := h.devs.Create(ctx, "d4", sep2.EndDevice{LFDI: d4, SFDI: "4"}); err != nil {
		t.Fatal(err)
	}
	now := powerNow
	for i, d := range []string{"d4", "d2", "d1"} {
		h.issue(ceScope(d), "c"+d, "MR-"+d, now-600, 600, ceTarget(int16(100*(i+1))), dercontrol.LifecycleRecord{})
	}

	fleet := &AdminFleetHandler{Managers: h.mgr}
	groups, err := energyGroupsOf(ctx, fleet, sortedAggregators(ctx, fleet))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, g := range groups {
		want = append(want, g.key)
	}
	_, got := h.get("window=900")
	var lanes []string
	for _, e := range got.Events {
		lanes = append(lanes, e.Group)
	}
	if len(want) != 3 || len(lanes) != 3 || lanes[0] != want[0] || lanes[1] != want[1] || lanes[2] != want[2] || want[2] != "direct" {
		t.Errorf("lanes = %v, want the power groups %v", lanes, want)
	}
	if want[0] != canonicalLFDI(powerAgg) || want[1] != aggB {
		t.Errorf("power groups = %v, want [%s %s direct]", want, canonicalLFDI(powerAgg), aggB)
	}
}

// The timeline looks ahead past now by the window length.
func TestControlEvents_LooksAheadByTheWindow(t *testing.T) {
	h := newCEHarness(t)
	now := powerNow
	h.issue(ceScope("d2"), "soon", "MR-SOON", now+300, 600, ceTarget(1000), dercontrol.LifecycleRecord{})
	h.issue(ceScope("d2"), "far", "MR-FAR", now+901, 600, ceTarget(1000), dercontrol.LifecycleRecord{})

	_, got := h.get("window=900")
	if got.WindowEnd != now || got.Ahead != 900 || got.AheadEnd != now+900 {
		t.Errorf("windowEnd/ahead/aheadEnd = %d/%d/%d, want %d/900/%d", got.WindowEnd, got.Ahead, got.AheadEnd, now, now+900)
	}
	if len(got.Events) != 1 || got.Events[0].MRID != "MR-SOON" || got.Events[0].Status != "scheduled" || got.Events[0].Start != now+300 {
		t.Fatalf("events = %+v, want only the scheduled MR-SOON starting at %d", got.Events, now+300)
	}
	_, long := h.get("window=3600")
	if len(long.Events) != 2 || long.AheadEnd != now+3600 {
		t.Errorf("1 h window events = %d aheadEnd %d, want both controls and %d", len(long.Events), long.AheadEnd, now+3600)
	}
}
