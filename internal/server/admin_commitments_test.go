package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

type commitmentsBody struct {
	AggregatorLFDI string `json:"aggregatorLFDI"`
	Now            int64  `json:"now"`
	Grants         []struct {
		MRID              string    `json:"mRID"`
		EdevID            string    `json:"edevId"`
		FrqID             string    `json:"frqId"`
		Window            sepWindow `json:"window"`
		Direction         *string   `json:"direction"`
		PowerW            *float64  `json:"powerW"`
		EnergyWh          *float64  `json:"energyWh"`
		EnergyRemainingWh *float64  `json:"energyRemainingWh"`
	} `json:"grants"`
	PlainControls []struct {
		MRID    string    `json:"mRID"`
		EdevID  string    `json:"edevId"`
		Window  sepWindow `json:"window"`
		TargetW *float64  `json:"targetW"`
	} `json:"plainControls"`
}

type sepWindow struct {
	Start    int64  `json:"start"`
	Duration uint32 `json:"duration"`
}

func getCommitments(t *testing.T, admin http.Handler) commitmentsBody {
	t.Helper()
	w := httptest.NewRecorder()
	admin.ServeHTTP(w, bearerFromLoopbackRequest(http.MethodGet, "/api/derms/commitments?aggregatorLFDI="+frAgreeLFDI, "", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("GET commitments = %d %s, want 200", w.Code, w.Body.String())
	}
	var body commitmentsBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	return body
}

// commitmentsFixture adds to frAgreementFixture's three responses (scheduled
// at now+3600, active since now-100, cancelled) a grant with no power, a
// revision, an ended grant, an execution of the active grant and one of the
// cancelled grant. It returns the clock the records were stored against.
func commitmentsFixture(t *testing.T) (http.Handler, *server.Stores, int64) {
	t.Helper()
	_, admin, stores := frAgreementFixture(t)
	ctx := context.Background()
	now := sep2time.Now().Unix()

	response := func(id string, start int64, power *sep2.ActivePower) {
		frp := sep2.FlowReservationResponse{
			Event: sep2.Event{
				SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev/frp/" + id}},
				MRID:                 "R-" + id, CreationTime: now - 50, Interval: &sep2.DateTimeInterval{Start: start, Duration: 3600},
			},
			EnergyAvailable: &sep2.SignedRealEnergy{Value: 1000}, PowerAvailable: power, Subject: "M-" + id,
		}
		if err := stores.FlowReservationResponses.Create(ctx, "dev", id, frp); err != nil {
			t.Fatal(err)
		}
	}
	response("frq-nopower", now+7200, nil)
	response("frq-rev-r1", now+10800, &sep2.ActivePower{Value: 3, Multiplier: 2})
	response("frq-ended", now-7200, &sep2.ActivePower{Value: 500})

	control := func(id, grant string, start int64, target int16, cancelled bool) {
		ctrl := sep2.DERControl{
			RandomizableEvent: sep2.RandomizableEvent{Event: sep2.Event{
				SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/dev/fsa/1/derp/1/derc/" + id}},
				MRID:                 "C-" + id, CreationTime: now - 60, Interval: &sep2.DateTimeInterval{Start: start, Duration: 600},
			}},
			DERControlBase: &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: target}},
		}
		if err := stores.DERControls.Create(ctx, "dev/1/1", id, ctrl); err != nil {
			t.Fatal(err)
		}
		lc := dercontrol.LifecycleRecord{GrantMRID: grant, FleetKey: frAgreeLFDI, Reach: 1}
		if cancelled {
			at := now - 10
			lc.CancelledAt = &at
		}
		if err := stores.DERControlLifecycles.Create(ctx, "dev/1/1", id, lc); err != nil {
			t.Fatal(err)
		}
	}
	control("exec-active", "R-frq-active", now-100, -360, false)
	control("exec-active-cancelled", "R-frq-active", now+500, -360, true)
	control("exec-of-cancelled", "R-frq-cancelled", now+3600, -200, false)
	control("plain-ended", "", now-1000, 100, false)
	return admin, stores, now
}

// Each live grant is listed with the field values the route serves, and
// nothing the ledger does not count: the cancelled and ended grants are
// absent, and the active grant's execution is under its grant, not plain.
func TestAdminCommitmentsListsLiveGrants(t *testing.T) {
	admin, _, now := commitmentsFixture(t)
	body := getCommitments(t, admin)

	if body.AggregatorLFDI != frAgreeLFDI || body.Now < now || body.Now > now+60 {
		t.Fatalf("aggregatorLFDI %q now %d, want %q and a now from %d", body.AggregatorLFDI, body.Now, frAgreeLFDI, now)
	}
	type grant struct {
		mrid, frq   string
		start       int64
		power       *float64
		energy, rem float64
	}
	f := func(v float64) *float64 { return &v }
	want := []grant{
		{"R-frq-active", "frq-active", now - 100, f(500), 1000, 940}, // 360 W x 600 s = 60 Wh committed
		{"R-frq-scheduled", "frq-scheduled", now + 3600, f(500), 1000, 1000},
		{"R-frq-nopower", "frq-nopower", now + 7200, nil, 1000, 1000},
		{"R-frq-rev-r1", "frq-rev", now + 10800, f(300), 1000, 1000},
	}
	if len(body.Grants) != len(want) {
		t.Fatalf("grants = %+v, want %d", body.Grants, len(want))
	}
	for i, w := range want {
		g := body.Grants[i]
		if g.MRID != w.mrid || g.FrqID != w.frq || g.EdevID != "dev" || g.Window != (sepWindow{w.start, 3600}) {
			t.Errorf("grant %d = %s %s %s %+v, want %s %s dev {%d 3600}", i, g.MRID, g.FrqID, g.EdevID, g.Window, w.mrid, w.frq, w.start)
		}
		if g.Direction == nil || *g.Direction != "charge" {
			t.Errorf("%s direction = %v, want charge (energyAvailable is charging positive)", g.MRID, g.Direction)
		}
		if (g.PowerW == nil) != (w.power == nil) || (g.PowerW != nil && *g.PowerW != *w.power) {
			t.Errorf("%s powerW = %v, want %v", g.MRID, g.PowerW, w.power)
		}
		if g.EnergyWh == nil || *g.EnergyWh != w.energy || g.EnergyRemainingWh == nil || *g.EnergyRemainingWh != w.rem {
			t.Errorf("%s energyWh %v remaining %v, want %v and %v", g.MRID, g.EnergyWh, g.EnergyRemainingWh, w.energy, w.rem)
		}
	}
}

// A granted response without power is still a commitment, listed with
// powerW null rather than dropped or shown as 0.
func TestAdminCommitmentsListsAGrantWithoutPower(t *testing.T) {
	admin, _, now := commitmentsFixture(t)
	body := getCommitments(t, admin)
	for _, g := range body.Grants {
		if g.MRID != "R-frq-nopower" {
			continue
		}
		if g.PowerW != nil || g.Window.Start != now+7200 || g.EnergyWh == nil || *g.EnergyWh != 1000 {
			t.Fatalf("R-frq-nopower = powerW %v start %d energyWh %v, want null, %d, 1000", g.PowerW, g.Window.Start, g.EnergyWh, now+7200)
		}
		return
	}
	t.Fatalf("grants = %+v, want R-frq-nopower among them", body.Grants)
}

// An execution of a cancelled grant is a plain control now, as the ledger
// counts it; the ended plain control and the cancelled execution are not.
func TestAdminCommitmentsListsAnExecutionOfACancelledGrantAsPlain(t *testing.T) {
	admin, _, now := commitmentsFixture(t)
	body := getCommitments(t, admin)
	if len(body.PlainControls) != 1 {
		t.Fatalf("plainControls = %+v, want only C-exec-of-cancelled", body.PlainControls)
	}
	c := body.PlainControls[0]
	if c.MRID != "C-exec-of-cancelled" || c.EdevID != "dev" || c.Window != (sepWindow{now + 3600, 600}) || c.TargetW == nil || *c.TargetW != -200 {
		t.Errorf("plain control = %s %s %+v targetW %v, want C-exec-of-cancelled dev {%d 600} -200", c.MRID, c.EdevID, c.Window, c.TargetW, now+3600)
	}
	for _, g := range body.Grants {
		if g.MRID == "R-frq-cancelled" {
			t.Errorf("the cancelled grant is listed: %+v", g)
		}
	}
}

// A fleet with nothing committed serves empty lists, never null.
func TestAdminCommitmentsEmptyFleetServesEmptyLists(t *testing.T) {
	_, admin, stores := frAgreementFixture(t)
	if err := stores.EndDevices.Create(context.Background(), "free", sep2.EndDevice{LFDI: "BBBB000000000000000000000000000000000002"}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	admin.ServeHTTP(w, bearerFromLoopbackRequest(http.MethodGet, "/api/derms/commitments?aggregatorLFDI=BBBB000000000000000000000000000000000002", "", ""))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil || w.Code != http.StatusOK {
		t.Fatalf("GET = %d %v %s", w.Code, err, w.Body.String())
	}
	if string(raw["grants"]) != "[]" || string(raw["plainControls"]) != "[]" {
		t.Errorf("grants %s plainControls %s, want [] and []", raw["grants"], raw["plainControls"])
	}
}

// The route writes nothing: every record it reads is byte for byte as it
// was after two reads.
func TestAdminCommitmentsWritesNothing(t *testing.T) {
	admin, stores, _ := commitmentsFixture(t)
	ctx := context.Background()
	snapshot := func() string {
		t.Helper()
		frp, err := stores.FlowReservationResponses.List(ctx, "dev", store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		frpLC, err := stores.FlowReservationResponseLifecycles.List(ctx, "dev", store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		ctrls, err := stores.DERControls.List(ctx, "dev/1/1", store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		ctrlLC, err := stores.DERControlLifecycles.List(ctx, "dev/1/1", store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal([]any{frp, frpLC, ctrls, ctrlLC})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	before := snapshot()
	getCommitments(t, admin)
	getCommitments(t, admin)
	if after := snapshot(); after != before {
		t.Errorf("records changed across two reads:\nbefore %s\nafter  %s", before, after)
	}
}

// An aggregatorLFDI that names no fleet is a 404 with a fixed body, not the
// empty lists a free fleet serves, and leaves the ledger's lock map as it
// was however many such keys arrive. A managed device's own LFDI names no
// fleet; a manager names one even with no EndDevice of its own.
func TestAdminCommitmentsUnknownFleetIs404(t *testing.T) {
	admin, stores, _ := commitmentsFixture(t)
	ctx := context.Background()
	const managed = "D001000000000000000000000000000000000001"
	const manager = "E001000000000000000000000000000000000001"
	if err := stores.EndDevices.Create(ctx, "managed", sep2.EndDevice{LFDI: managed}); err != nil {
		t.Fatal(err)
	}
	if err := stores.EndDeviceManagers.Assign(ctx, manager, managed); err != nil {
		t.Fatal(err)
	}
	get := func(lfdi string) (int, string, string) {
		w := httptest.NewRecorder()
		admin.ServeHTTP(w, bearerFromLoopbackRequest(http.MethodGet, "/api/derms/commitments?aggregatorLFDI="+lfdi, "", ""))
		var body struct{ Error, Code string }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		return w.Code, body.Code, body.Error
	}

	getCommitments(t, admin) // the known fleet's lock exists before the count
	before := stores.CommitmentLedger.FleetLockCount()
	for i := range 500 {
		lfdi := fmt.Sprintf("%040X", i+1)
		if code, c, e := get(lfdi); code != http.StatusNotFound || c != "fleet_not_found" || e != "no such fleet" {
			t.Fatalf("unknown fleet %s = %d %s %q, want 404 fleet_not_found", lfdi, code, c, e)
		}
	}
	if code, c, _ := get(managed); code != http.StatusNotFound || c != "fleet_not_found" {
		t.Errorf("a managed device's LFDI = %d %s, want 404", code, c)
	}
	if after := stores.CommitmentLedger.FleetLockCount(); after != before {
		t.Errorf("fleet locks = %d after 501 unknown keys, want the %d before", after, before)
	}
	if code, _, _ := get(manager); code != http.StatusOK {
		t.Errorf("a manager without its own EndDevice = %d, want 200", code)
	}
}
