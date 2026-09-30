package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// The fleet these tests commit: an aggregator EndDevice "agg" whose own
// LFDI is the fleet key, managing device 0 (dcLFDI). Device 1 is unmanaged,
// so it is a fleet of its own.
const (
	aggDevice = "agg"
	aggLFDI   = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	grantMRID = "0123456789ABCDEF0123456789ABCDEF"
)

// grantSpec is a FlowReservationResponse as stored: energy charging
// positive (watt-hours), power a magnitude.
type grantSpec struct {
	start     int64
	duration  uint32
	energyWh  int64
	powerW    *int16
	cancelled bool
}

func ptrI16(v int16) *int16 { return &v }

// seedFleet makes device 0 a managed device of the aggregator and gives
// device 1 a program, so an execution can be aimed outside the fleet.
func (d *dcHarness) seedFleet(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if err := d.devices.Create(ctx, aggDevice, sep2.EndDevice{LFDI: aggLFDI, SFDI: "2"}); err != nil {
		t.Fatal(err)
	}
	if err := d.managers.Assign(ctx, aggLFDI, dcLFDI); err != nil {
		t.Fatal(err)
	}
	d.seedProgram(t, dcDeviceB, "0", "0", "PROGRAM-B", 1, true)
}

// seedGrant stores a response under the aggregator's own EndDevice, where a
// reservation is posted.
func (d *dcHarness) seedGrant(t *testing.T, g grantSpec) {
	t.Helper()
	ctx := context.Background()
	frp := sep2.FlowReservationResponse{}
	frp.Href = "/edev/" + aggDevice + "/frp/R1"
	frp.MRID = grantMRID
	frp.Interval = &sep2.DateTimeInterval{Start: g.start, Duration: g.duration}
	frp.EnergyAvailable = &sep2.SignedRealEnergy{Value: g.energyWh}
	if g.powerW != nil {
		frp.PowerAvailable = &sep2.ActivePower{Value: *g.powerW}
	}
	if err := d.grants.Create(ctx, aggDevice, "R1", frp); err != nil {
		t.Fatal(err)
	}
	if g.cancelled {
		at := g.start - 1
		if err := d.grantMarks.Create(ctx, aggDevice, "R1", dercontrol.LifecycleRecord{CancelledAt: &at}); err != nil {
			t.Fatal(err)
		}
	}
}

func targetWBody(edev string, targetW int, start int64, duration int, grant string) string {
	b := fmt.Sprintf(`{"derProgramHref":"/edev/%s/fsa/0/derp/0","type":"targetW","targetW":{"value":%d,"multiplier":0},"startTime":%d,"durationSeconds":%d`, edev, targetW, start, duration)
	if grant != "" {
		b += fmt.Sprintf(`,"executesGrant":%q`, grant)
	}
	return b + "}"
}

type conflictBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
	MRID  string `json:"mRID"`
}

// assertConflict checks a 409's status and every body field, and that the
// body carries nothing else.
func assertConflict(t *testing.T, w *httptest.ResponseRecorder, code, message, mrid string) {
	t.Helper()
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d body = %s, want 409", w.Code, w.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("409 body is not JSON: %v (%s)", err, w.Body.String())
	}
	if len(raw) != 3 {
		t.Errorf("409 body = %s, want exactly error, code and mRID", w.Body.String())
	}
	var got conflictBody
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got != (conflictBody{Error: message, Code: code, MRID: mrid}) {
		t.Errorf("409 body = %+v, want code %q message %q mRID %q", got, code, message, mrid)
	}
}

func (d *dcHarness) assertNothingStored(t *testing.T) {
	t.Helper()
	if c, l := d.storedCounts(t); c != 0 || l != 0 {
		t.Errorf("stored %d controls and %d lifecycle records after a refusal, want none", c, l)
	}
	parents, err := d.lifecycles.Parents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(parents) != 0 {
		t.Errorf("lifecycle scopes %v after a refusal, want none", parents)
	}
}

// #566 comment criterion 1 at the route: every bound of a grant refuses one
// step past it with 409 naming the grant, and stores nothing. The grant is a
// charge (energy positive), so its executions carry a negative target:
// 100 Wh is 360000 Ws, so -1000 W fits 360 s and not 361 s.
func TestDERControlCreate_ExecutionBounds(t *testing.T) {
	gStart := futureStart(600)
	live := grantSpec{start: gStart, duration: 3600, energyWh: 100, powerW: ptrI16(5000)}

	cases := []struct {
		name    string
		grant   grantSpec
		body    string
		code    string
		message string
		mrid    string
	}{
		{"grant cancelled", grantSpec{start: gStart, duration: 3600, energyWh: 100, powerW: ptrI16(5000), cancelled: true},
			targetWBody("0", -1000, gStart, 60, grantMRID), "grant_not_live", "executesGrant: grant is not live", grantMRID},
		{"grant absent", live,
			targetWBody("0", -1000, gStart, 60, strings.Repeat("F", 32)), "grant_not_live", "executesGrant: grant is not live", strings.Repeat("F", 32)},
		{"grant without power", grantSpec{start: gStart, duration: 3600, energyWh: 100},
			targetWBody("0", -1000, gStart, 60, grantMRID), "grant_not_executable", "executesGrant: grant has no interval, energy or power to execute", grantMRID},
		{"mode not targetW", live,
			fmt.Sprintf(`{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"maxLimW","maxLimW":100,"startTime":%d,"durationSeconds":60,"executesGrant":%q}`, gStart, grantMRID),
			"execution_mode_not_target_w", "type: a control carrying out a grant must be targetW", grantMRID},
		{"outside the fleet", live,
			targetWBody(dcDeviceB, -1000, gStart, 60, grantMRID), "execution_outside_fleet", "executesGrant: grant belongs to another fleet", grantMRID},
		{"starts one second before the interval", live,
			targetWBody("0", -1000, gStart-1, 60, grantMRID), "execution_outside_interval", "startTime, durationSeconds: outside the grant's interval", grantMRID},
		{"ends one second after the interval", live,
			targetWBody("0", -1000, gStart+3600-59, 60, grantMRID), "execution_outside_interval", "startTime, durationSeconds: outside the grant's interval", grantMRID},
		{"charge grant, positive target", live,
			targetWBody("0", 1, gStart, 60, grantMRID), "execution_reverses_grant", "targetW: sign reverses the grant's direction", grantMRID},
		{"one watt past powerAvailable", live,
			targetWBody("0", -5001, gStart, 60, grantMRID), "execution_exceeds_power", "targetW: exceeds the grant's powerAvailable", grantMRID},
		{"one second past energyAvailable", live,
			targetWBody("0", -1000, gStart, 361, grantMRID), "execution_exceeds_energy", "targetW: exceeds the grant's energyAvailable", grantMRID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			d.seedFleet(t)
			d.seedGrant(t, tc.grant)
			w := d.do(t, http.MethodPost, "/api/der/controls", tc.body)
			assertConflict(t, w, tc.code, tc.message, tc.mrid)
			d.assertNothingStored(t)
			if n := d.notifier.take(); len(n) != 0 {
				t.Errorf("notifications %v after a refusal, want none", n)
			}
			out := d.logs.String()
			if strings.Count(out, "\n") != 1 || !strings.Contains(out, "level=WARN") || !strings.Contains(out, "code="+tc.code) ||
				!strings.Contains(out, "status=409") || !strings.Contains(out, "conflict_mrid="+tc.mrid) {
				t.Errorf("log = %s", out)
			}
		})
	}
}

// The step inside each bound is accepted, so the refusals above are the
// bounds and not a blanket refusal.
func TestDERControlCreate_ExecutionAtEachBound(t *testing.T) {
	gStart := futureStart(600)
	live := grantSpec{start: gStart, duration: 3600, energyWh: 100, powerW: ptrI16(5000)}
	for _, tc := range []struct {
		name string
		body string
	}{
		{"starts with the interval", targetWBody("0", -1000, gStart, 60, grantMRID)},
		{"ends with the interval", targetWBody("0", -1000, gStart+3600-60, 60, grantMRID)},
		{"exactly powerAvailable", targetWBody("0", -5000, gStart, 60, grantMRID)},
		{"exactly energyAvailable", targetWBody("0", -1000, gStart, 360, grantMRID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			d.seedFleet(t)
			d.seedGrant(t, live)
			if w := d.do(t, http.MethodPost, "/api/der/controls", tc.body); w.Code != http.StatusCreated {
				t.Fatalf("status = %d body = %s, want 201", w.Code, w.Body.String())
			}
		})
	}
}

// The sign trap, both ways through the route: the response stores energy
// charging positive and opModTargetW is discharge positive, so a charge
// grant stores a negative target and a discharge grant a positive one.
// The stored control, its link and the 201 body are all checked.
func TestDERControlCreate_ExecutionStoresTargetAndLink(t *testing.T) {
	gStart := futureStart(600)
	for _, tc := range []struct {
		name     string
		energyWh int64
		accept   int
		refuse   int
	}{
		{"charge grant", 100, -2000, 2000},
		{"discharge grant", -100, 2000, -2000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			d.seedFleet(t)
			d.seedGrant(t, grantSpec{start: gStart, duration: 3600, energyWh: tc.energyWh, powerW: ptrI16(5000)})

			w := d.do(t, http.MethodPost, "/api/der/controls", targetWBody("0", tc.refuse, gStart, 60, grantMRID))
			assertConflict(t, w, "execution_reverses_grant", "targetW: sign reverses the grant's direction", grantMRID)
			d.assertNothingStored(t)

			w = d.do(t, http.MethodPost, "/api/der/controls", targetWBody("0", tc.accept, gStart, 60, grantMRID))
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d body = %s, want 201", w.Code, w.Body.String())
			}
			got := decodeCreated(t, w)
			if got.ExecutesGrant == nil || *got.ExecutesGrant != grantMRID {
				t.Errorf("executesGrant = %v, want %s", got.ExecutesGrant, grantMRID)
			}
			if got.Type != "targetW" || got.DERControlBase.OpModTargetW == nil ||
				*got.DERControlBase.OpModTargetW != (handler.ActivePowerView{Value: int16(tc.accept), Multiplier: 0}) {
				t.Errorf("201 type %q base %+v, want targetW %d", got.Type, got.DERControlBase, tc.accept)
			}

			ctx := context.Background()
			parent, id, stored, err := d.controls.ByMRID(ctx, got.MRID)
			if err != nil {
				t.Fatal(err)
			}
			if b := stored.DERControlBase; b == nil || b.OpModTargetW == nil || b.OpModTargetW.Value != int16(tc.accept) || b.OpModTargetW.Multiplier != 0 ||
				b.OpModMaxLimW != nil || b.OpModConnect != nil || b.OpModFixedPFInjectW != nil {
				t.Errorf("stored base = %+v, want only opModTargetW %d", stored.DERControlBase, tc.accept)
			}
			if stored.Interval == nil || stored.Interval.Start != gStart || stored.Interval.Duration != 60 {
				t.Errorf("stored interval = %+v", stored.Interval)
			}
			lc, err := d.lifecycles.Get(ctx, parent, id)
			if err != nil {
				t.Fatal(err)
			}
			if lc.GrantMRID != grantMRID || lc.FleetKey != aggLFDI || lc.Reach != 1 || lc.CancelledAt != nil || lc.SupersededAt != nil {
				t.Errorf("lifecycle = %+v, want grant %s, fleet %s, reach 1", lc, grantMRID, aggLFDI)
			}
			if !strings.Contains(d.logs.String(), "executes_grant="+grantMRID) {
				t.Errorf("created line does not name the grant: %s", d.logs.String())
			}

			list := d.do(t, http.MethodGet, "/api/der/controls?device=0", "")
			var listed handler.DERControlList
			if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
				t.Fatal(err)
			}
			if len(listed.Controls) != 1 || listed.Controls[0].ExecutesGrant == nil || *listed.Controls[0].ExecutesGrant != grantMRID {
				t.Errorf("list = %s, want the control naming its grant", list.Body.String())
			}
		})
	}
}

// #566 comment criterion 2: a plain control over a live grant of its fleet
// is refused naming the grant and stores nothing; touching the grant's end,
// another fleet, and a cancelled grant are all free.
func TestDERControlCreate_PlainOverGrant(t *testing.T) {
	gStart := futureStart(600)
	grant := grantSpec{start: gStart, duration: 3600, energyWh: 100, powerW: ptrI16(5000)}
	connect := func(edev string, start int64, duration int) string {
		return fmt.Sprintf(`{"derProgramHref":"/edev/%s/fsa/0/derp/0","type":"connect","startTime":%d,"durationSeconds":%d}`, edev, start, duration)
	}

	t.Run("overlap by one second is refused", func(t *testing.T) {
		d := newDCHarness(t, ptrU32(dcPEN))
		d.seedFleet(t)
		d.seedGrant(t, grant)
		w := d.do(t, http.MethodPost, "/api/der/controls", connect("0", gStart+3600-1, 60))
		assertConflict(t, w, "fleet_window_committed", "control overlaps a live flow reservation grant of its fleet", grantMRID)
		d.assertNothingStored(t)
	})
	for _, tc := range []struct {
		name      string
		cancelled bool
		body      string
	}{
		{"touching the grant's end", false, connect("0", gStart+3600, 60)},
		{"another fleet", false, connect(dcDeviceB, gStart, 60)},
		{"cancelled grant", true, connect("0", gStart, 60)},
	} {
		t.Run(tc.name+" is accepted", func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			d.seedFleet(t)
			g := grant
			g.cancelled = tc.cancelled
			d.seedGrant(t, g)
			w := d.do(t, http.MethodPost, "/api/der/controls", tc.body)
			if w.Code != http.StatusCreated {
				t.Fatalf("status = %d body = %s, want 201", w.Code, w.Body.String())
			}
			if got := decodeCreated(t, w); got.ExecutesGrant != nil {
				t.Errorf("a plain control names grant %q", *got.ExecutesGrant)
			}
			if !strings.Contains(w.Body.String(), `"executesGrant":null`) {
				t.Errorf("201 body = %s, want executesGrant null for a plain control", w.Body.String())
			}
		})
	}
}

// failingGrantLister is a response store that cannot be listed.
type failingGrantLister struct{}

func (failingGrantLister) Parents(context.Context) ([]string, error) {
	return nil, errors.New("response store /var/lib/sep2 unavailable")
}

func (failingGrantLister) List(context.Context, string, store.ListOptions) (store.ListResult[sep2.FlowReservationResponse], error) {
	return store.ListResult[sep2.FlowReservationResponse]{}, errors.New("response store /var/lib/sep2 unavailable")
}

// A check that cannot complete takes the internal-error path, never a
// conflict, and never passes: a store error, an unresolvable fleet and an
// unwired ledger each answer 500 with nothing stored.
func TestDERControlCreate_CommitmentCheckFailsClosed(t *testing.T) {
	gStart := futureStart(600)
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, d *dcHarness)
		body  string
	}{
		{"grant store fails, plain control", func(t *testing.T, d *dcHarness) {
			d.h.Ledger = sources.NewLedger(d.devices, d.managers, failingGrantLister{}, d.grantMarks, d.controls, d.lifecycles)
		}, maxLimWBody(gStart, 100, 60)},
		{"grant store fails, execution", func(t *testing.T, d *dcHarness) {
			d.h.Ledger = sources.NewLedger(d.devices, d.managers, failingGrantLister{}, d.grantMarks, d.controls, d.lifecycles)
		}, targetWBody("0", -1000, gStart, 60, grantMRID)},
		{"device has no LFDI", func(t *testing.T, d *dcHarness) {
			if err := d.devices.Update(context.Background(), dcDevice, sep2.EndDevice{SFDI: "1"}); err != nil {
				t.Fatal(err)
			}
		}, maxLimWBody(gStart, 100, 60)},
		{"no ledger", func(t *testing.T, d *dcHarness) { d.h.Ledger = nil }, maxLimWBody(gStart, 100, 60)},
		{"no fleet resolver", func(t *testing.T, d *dcHarness) { d.h.Fleets = nil }, maxLimWBody(gStart, 100, 60)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			d.seedFleet(t)
			tc.setup(t, d)
			w := d.do(t, http.MethodPost, "/api/der/controls", tc.body)
			assertRefusal(t, w, http.StatusInternalServerError, "internal error")
			d.assertNothingStored(t)
			out := d.logs.String()
			if !strings.Contains(out, "cause=commitment_check_failed") || !strings.Contains(out, "level=ERROR") || strings.Contains(out, "/var/lib") {
				t.Errorf("log = %s", out)
			}
		})
	}
}

// A program under an EndDevice that does not exist is still the issuer's
// 404, not a failed fleet lookup.
func TestDERControlCreate_UnknownDeviceIsProgramNotFound(t *testing.T) {
	d := newDCHarness(t, ptrU32(dcPEN))
	w := d.do(t, http.MethodPost, "/api/der/controls", `{"derProgramHref":"/edev/9/fsa/0/derp/0","type":"connect","durationSeconds":300}`)
	assertRefusal(t, w, http.StatusNotFound, "derProgramHref: DERProgram not found")
	d.assertNothingStored(t)
}

// targetW and executesGrant are validated field by field like every other
// field, and never echo the submitted value.
func TestDERControlCreate_TargetWRefusals(t *testing.T) {
	const marker = "SUBMITTED9731"
	base := `{"derProgramHref":"/edev/0/fsa/0/derp/0","durationSeconds":300,`
	for _, tc := range []struct {
		name, body, want string
	}{
		{"targetW missing", base + `"type":"targetW"}`, "targetW: required for type targetW"},
		{"targetW value missing", base + `"type":"targetW","targetW":{"multiplier":0}}`, "targetW.value: required"},
		{"targetW value over int16", base + `"type":"targetW","targetW":{"value":32768}}`, "targetW.value: must be -32768 to 32767"},
		{"targetW value under int16", base + `"type":"targetW","targetW":{"value":-32769}}`, "targetW.value: must be -32768 to 32767"},
		{"targetW multiplier out of range", base + `"type":"targetW","targetW":{"value":1,"multiplier":10}}`, "targetW.multiplier: must be -9 to 9"},
		{"targetW value wrong type", base + `"type":"targetW","targetW":{"value":"` + marker + `"}}`, "targetW.value: must be an integer"},
		{"targetW not an object", base + `"type":"targetW","targetW":"` + marker + `"}`, "targetW: must be an object"},
		{"targetW on maxLimW", base + `"type":"maxLimW","maxLimW":5,"targetW":{"value":1}}`, "targetW: not allowed for this type"},
		{"maxLimW on targetW", base + `"type":"targetW","maxLimW":5,"targetW":{"value":1}}`, "maxLimW: not allowed for this type"},
		{"powerFactor on targetW", base + `"type":"targetW","powerFactor":{"displacement":1,"excitation":true},"targetW":{"value":1}}`, "powerFactor: not allowed for this type"},
		{"executesGrant bad format", base + `"type":"targetW","targetW":{"value":-1},"executesGrant":"` + marker + `"}`, "executesGrant: invalid format"},
		{"executesGrant empty", base + `"type":"targetW","targetW":{"value":-1},"executesGrant":""}`, "executesGrant: invalid format"},
		{"executesGrant wrong type", base + `"type":"targetW","targetW":{"value":-1},"executesGrant":7}`, "executesGrant: must be a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDCHarness(t, ptrU32(dcPEN))
			w := d.do(t, http.MethodPost, "/api/der/controls", tc.body)
			assertRefusal(t, w, http.StatusBadRequest, tc.want)
			d.assertNothingStored(t)
			if strings.Contains(w.Body.String(), marker) || strings.Contains(d.logs.String(), marker) {
				t.Errorf("submitted value echoed: body %s log %s", w.Body.String(), d.logs.String())
			}
		})
	}
}

// A plain targetW control with no grant anywhere is stored like any other
// type, and lower-case executesGrant hex names the same grant.
func TestDERControlCreate_TargetWPlainAndLowerCaseGrant(t *testing.T) {
	gStart := futureStart(600)
	d := newDCHarness(t, ptrU32(dcPEN))
	w := d.do(t, http.MethodPost, "/api/der/controls", targetWBody("0", 1500, gStart, 60, ""))
	if w.Code != http.StatusCreated {
		t.Fatalf("plain targetW: status = %d body = %s", w.Code, w.Body.String())
	}

	d = newDCHarness(t, ptrU32(dcPEN))
	d.seedFleet(t)
	d.seedGrant(t, grantSpec{start: gStart, duration: 3600, energyWh: 100, powerW: ptrI16(5000)})
	w = d.do(t, http.MethodPost, "/api/der/controls", targetWBody("0", -1000, gStart, 60, strings.ToLower(grantMRID)))
	if w.Code != http.StatusCreated {
		t.Fatalf("lower-case grant: status = %d body = %s", w.Code, w.Body.String())
	}
	if got := decodeCreated(t, w); got.ExecutesGrant == nil || *got.ExecutesGrant != grantMRID {
		t.Errorf("executesGrant = %v, want %s", got.ExecutesGrant, grantMRID)
	}
}
