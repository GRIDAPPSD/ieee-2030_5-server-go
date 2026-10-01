package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment/sources"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #764: admin read API for flow reservations.

const (
	frAggLFDI      = "3E4F45AB31EDFE5B67E343E5E4562E31984E23E5"
	frOtherAggLFDI = "BBBB000000000000000000000000000000000002"
	frManagedLFDI  = "D001000000000000000000000000000000000001"
	frCert         = "cert:5D9A0C1B7E3F2A4D6C8B0E1F3A5C7E9B1D3F5A7C9E1B3D5F7A9C1E3B5D7F9A1C"
	frNow          = int64(1790000100)
)

type frAttrKey struct{ edev, id string }

type frAttr struct{ answered, cancelled *flowreservation.Attribution }

type fakeFRAttributions struct {
	byResponse map[frAttrKey]frAttr
	err        error
}

func (f fakeFRAttributions) AttributionsOf(_ context.Context, edevID, id string) (*flowreservation.Attribution, *flowreservation.Attribution, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	a := f.byResponse[frAttrKey{edevID, id}]
	return a.answered, a.cancelled, nil
}

type frFixture struct {
	t       *testing.T
	devices *memory.EndDeviceStore
	pairs   *memory.EndDeviceManagementStore
	frqs    *memory.ScopedStore[sep2.FlowReservationRequest]
	frps    *memory.ScopedStore[sep2.FlowReservationResponse]
	lcs     *memory.ScopedStore[dercontrol.LifecycleRecord]
	ctrls   *memory.DERControlStore
	ctrlLcs *dercontrol.LifecycleStore
	attrs   fakeFRAttributions
}

func newFRFixture(t *testing.T) *frFixture {
	t.Helper()
	f := &frFixture{
		t:       t,
		devices: memory.NewEndDeviceStore(),
		pairs:   memory.NewEndDeviceManagementStore(),
		frqs:    memory.NewScopedStore[sep2.FlowReservationRequest](),
		frps:    memory.NewScopedStore[sep2.FlowReservationResponse](),
		lcs:     memory.NewScopedStore[dercontrol.LifecycleRecord](),
		ctrls:   memory.NewDERControlStore(),
		ctrlLcs: dercontrol.NewLifecycleStore(),
		attrs:   fakeFRAttributions{byResponse: map[frAttrKey]frAttr{}},
	}
	f.device("4", frAggLFDI)
	return f
}

func (f *frFixture) device(id, lfdi string) {
	f.t.Helper()
	if err := f.devices.Create(context.Background(), id, sep2.EndDevice{LFDI: lfdi}); err != nil {
		f.t.Fatal(err)
	}
}

// handler builds the handler as internal/server wires it: the response and
// control stores wrapped the way the protocol routes wrap them.
func (f *frFixture) handler() *handler.AdminFlowReservationHandler {
	fleets := commitment.Resolver{Devices: f.devices, Managers: f.pairs}
	return &handler.AdminFlowReservationHandler{
		Requests:     f.frqs,
		Responses:    flowreservation.NewDerivedStatusResponseStore(f.frps, f.lcs),
		Lifecycles:   f.lcs,
		Fleets:       fleets,
		Executions:   sources.NewControls(f.ctrls, f.ctrlLcs, fleets),
		Controls:     coreder.NewDerivedStatusControlStore(f.ctrls, f.ctrlLcs),
		Attributions: f.attrs,
		Persisted:    true,
		Now:          func() int64 { return frNow },
	}
}

type frqSpec struct {
	edev, id, mrid string
	created        int64
	cancelled      bool
	interval       *sep2.DateTimeInterval
	energy         *sep2.SignedRealEnergy
	power          *sep2.ActivePower
}

func (f *frFixture) request(s frqSpec) {
	f.t.Helper()
	frq := sep2.FlowReservationRequest{
		Resource:          sep2.Resource{Href: "/edev/" + s.edev + "/frq/" + s.id},
		MRID:              s.mrid,
		CreationTime:      s.created,
		IntervalRequested: s.interval,
		EnergyRequested:   s.energy,
		PowerRequested:    s.power,
	}
	if s.cancelled {
		frq.RequestStatus = sep2.RequestStatus{RequestStatus: sep2.RequestStatusCancelled}
	}
	if err := f.frqs.Create(context.Background(), s.edev, s.id, frq); err != nil {
		f.t.Fatal(err)
	}
}

type frpSpec struct {
	edev, id, mrid, subject string
	created                 int64
	interval                *sep2.DateTimeInterval
	energy                  *sep2.SignedRealEnergy
	power                   *sep2.ActivePower
	cancelledAt             *int64
	reason                  string
}

func (f *frFixture) response(s frpSpec) {
	f.t.Helper()
	ctx := context.Background()
	frp := sep2.FlowReservationResponse{
		Event: sep2.Event{
			SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/" + s.edev + "/frp/" + s.id}},
			MRID:                 s.mrid,
			CreationTime:         s.created,
			Interval:             s.interval,
		},
		EnergyAvailable: s.energy,
		PowerAvailable:  s.power,
		Subject:         s.subject,
	}
	if err := f.frps.Create(ctx, s.edev, s.id, frp); err != nil {
		f.t.Fatal(err)
	}
	if s.cancelledAt != nil {
		if err := f.lcs.Create(ctx, s.edev, s.id, dercontrol.LifecycleRecord{CancelledAt: s.cancelledAt, CancelReason: s.reason}); err != nil {
			f.t.Fatal(err)
		}
	}
}

type ctrlSpec struct {
	scope, id, mrid, grant string
	created                int64
	window                 sep2.DateTimeInterval
	target                 int16
	reach                  int
	cancelledAt            *int64
}

func (f *frFixture) control(s ctrlSpec) {
	f.t.Helper()
	ctx := context.Background()
	parts := strings.Split(s.scope, "/")
	href := fmt.Sprintf("/edev/%s/fsa/%s/derp/%s/derc/%s", parts[0], parts[1], parts[2], s.id)
	ctrl := sep2.DERControl{
		RandomizableEvent: sep2.RandomizableEvent{Event: sep2.Event{
			SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: href}},
			MRID:                 s.mrid,
			CreationTime:         s.created,
			Interval:             &s.window,
		}},
		DERControlBase: &sep2.DERControlBase{OpModTargetW: &sep2.ActivePower{Value: s.target}},
	}
	if err := f.ctrls.Create(ctx, s.scope, s.id, ctrl); err != nil {
		f.t.Fatal(err)
	}
	lc := dercontrol.LifecycleRecord{GrantMRID: s.grant, FleetKey: frAggLFDI, Reach: s.reach, CancelledAt: s.cancelledAt}
	if err := f.ctrlLcs.Create(ctx, s.scope, s.id, lc); err != nil {
		f.t.Fatal(err)
	}
}

func (f *frFixture) attribute(edev, id string, answered, cancelled *flowreservation.Attribution) {
	f.attrs.byResponse[frAttrKey{edev, id}] = frAttr{answered: answered, cancelled: cancelled}
}

func ptr[T any](v T) *T { return &v }

// seedGolden stores the fleet the golden file describes: a pending request,
// and a granted one whose first response was cancelled and revised to a
// second, which one execution carries out.
func (f *frFixture) seedGolden() {
	f.request(frqSpec{
		edev: "4", id: "frq-1790000000000000001", mrid: "A1B2C3D4E5F60718293A4B5C6D7E8F90", created: 1790000000,
		interval: &sep2.DateTimeInterval{Start: 1790003600, Duration: 1800},
		energy:   &sep2.SignedRealEnergy{Value: 10000}, power: &sep2.ActivePower{Value: 20000},
	})
	f.request(frqSpec{
		edev: "4", id: "frq-1789990000000000002", mrid: "0F1E2D3C4B5A69788796A5B4C3D2E1F0", created: 1789990000,
		interval: &sep2.DateTimeInterval{Start: 1790000000, Duration: 3600},
		energy:   &sep2.SignedRealEnergy{Value: -8000}, power: &sep2.ActivePower{Value: 8000},
	})
	f.response(frpSpec{
		edev: "4", id: "frq-1789990000000000002", mrid: "4A2F7C9E00000000000000000000A1B2", subject: "0F1E2D3C4B5A69788796A5B4C3D2E1F0",
		created:  1789990200,
		interval: &sep2.DateTimeInterval{Start: 1790000000, Duration: 3600},
		energy:   &sep2.SignedRealEnergy{Value: -8000}, power: &sep2.ActivePower{Value: 8000},
		cancelledAt: ptr(int64(1789995000)), reason: "revised for feeder limit",
	})
	f.response(frpSpec{
		edev: "4", id: "frq-1789990000000000002-r1", mrid: "9D1E3B5C00000000000000000000A1B2", subject: "0F1E2D3C4B5A69788796A5B4C3D2E1F0",
		created:  1789995000,
		interval: &sep2.DateTimeInterval{Start: 1790000000, Duration: 1800},
		energy:   &sep2.SignedRealEnergy{Value: -4000}, power: &sep2.ActivePower{Value: 8000},
	})
	f.control(ctrlSpec{
		scope: "7/1/1", id: "3", mrid: "C0FFEE0000000000000000000000A1B2", grant: "9D1E3B5C00000000000000000000A1B2",
		created: 1789995100, window: sep2.DateTimeInterval{Start: 1790000000, Duration: 900}, target: 4000, reach: 1,
	})
	operator := flowreservation.Attribution{Kind: "operator", Admission: "mtls", Principal: frCert}
	first := operator
	first.At = 1789995000
	f.attribute("4", "frq-1789990000000000002",
		&flowreservation.Attribution{Kind: "deadline_fallback", At: 1789990200}, &first)
	second := operator
	second.At = 1789995000
	f.attribute("4", "frq-1789990000000000002-r1", &second, nil)
}

func frGet(t *testing.T, h http.HandlerFunc, target string, pathValues map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, v := range pathValues {
		req.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func frList(t *testing.T, f *frFixture, query string) FRListBody {
	t.Helper()
	w := frGet(t, f.handler().HandleList(), "/api/derms/flow-reservations?"+query, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list %q = %d %s, want 200", query, w.Code, w.Body.String())
	}
	var out FRListBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode list: %v\n%s", err, w.Body.String())
	}
	return out
}

// FRListBody mirrors only what the tests read back from a list.
type FRListBody struct {
	AggregatorLFDI string `json:"aggregatorLFDI"`
	Requests       []struct {
		EdevID     string `json:"edevId"`
		FrqID      string `json:"frqId"`
		State      string `json:"state"`
		DeadlineAt *int64 `json:"deadlineAt"`
		Tip        *struct {
			ID                string   `json:"id"`
			EnergyCommittedWh *float64 `json:"energyCommittedWh"`
			EnergyRemainingWh *float64 `json:"energyRemainingWh"`
		} `json:"tip"`
	} `json:"requests"`
}

func (b FRListBody) states() map[string]string {
	out := map[string]string{}
	for _, r := range b.Requests {
		out[r.FrqID] = r.State
	}
	return out
}

func TestFlowReservationListMatchesGolden(t *testing.T) {
	f := newFRFixture(t)
	f.seedGolden()
	w := frGet(t, f.handler().HandleList(), "/api/derms/flow-reservations?aggregatorLFDI="+frAggLFDI, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", w.Code, w.Body.String())
	}
	golden, err := os.ReadFile("testdata/flowreservation_list.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.Body.Bytes(), golden) {
		t.Errorf("list body differs from testdata/flowreservation_list.golden.json\n--- got ---\n%s", w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestFlowReservationFrontendFixtureIsTheGolden(t *testing.T) {
	golden, err := os.ReadFile("testdata/flowreservation_list.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../pkg/adminui/web/frontend/src/lib/flowreservation.fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(golden, fixture) {
		t.Error("the frontend fixture differs from the golden file: change both together")
	}
}

// Each state the view documents, seeded under one fleet, so a state filter
// and the state rules are pinned against their own record.
func seedEveryState(f *frFixture) {
	win := func(start int64, dur uint32) *sep2.DateTimeInterval {
		return &sep2.DateTimeInterval{Start: start, Duration: dur}
	}
	req := func(id string, created int64, start int64, cancelled bool) {
		f.request(frqSpec{edev: "4", id: id, mrid: "M-" + id, created: created, cancelled: cancelled,
			interval: win(start, 3600), energy: &sep2.SignedRealEnergy{Value: 4000}, power: &sep2.ActivePower{Value: 8000}})
	}
	grant := func(id string, start int64, dur uint32, cancelledAt *int64) {
		f.response(frpSpec{edev: "4", id: id, mrid: "R-" + id, subject: "M-" + id, created: start - 10,
			interval: win(start, dur), energy: &sep2.SignedRealEnergy{Value: 4000}, power: &sep2.ActivePower{Value: 8000},
			cancelledAt: cancelledAt})
	}
	req("frq-pending", frNow-10, frNow+9000, false) // deadline frNow+290
	req("frq-overdue", frNow-400, frNow+9000, false)
	req("frq-granted", frNow-50, frNow+100, false)
	grant("frq-granted", frNow+100, 3600, nil)
	req("frq-denied", frNow-50, frNow+100, false)
	grant("frq-denied", frNow+100, 0, nil)
	req("frq-cancelled", frNow-50, frNow+100, false)
	grant("frq-cancelled", frNow+100, 3600, ptr(frNow-5))
	req("frq-withdrawn", frNow-50, frNow+100, true)
	req("frq-ended", frNow-9000, frNow-8000, false)
	grant("frq-ended", frNow-8000, 600, nil)
}

func TestFlowReservationStates(t *testing.T) {
	f := newFRFixture(t)
	seedEveryState(f)
	got := frList(t, f, "aggregatorLFDI="+frAggLFDI).states()
	want := map[string]string{
		"frq-pending": "pending", "frq-overdue": "overdue", "frq-granted": "granted", "frq-denied": "denied",
		"frq-cancelled": "cancelled", "frq-withdrawn": "withdrawn", "frq-ended": "ended",
	}
	for id, state := range want {
		if got[id] != state {
			t.Errorf("state of %s = %q, want %q", id, got[id], state)
		}
	}
	if len(got) != len(want) {
		t.Errorf("listed %d requests, want %d: %v", len(got), len(want), got)
	}
}

func TestFlowReservationDeadlineAtOnlyWhileUnanswered(t *testing.T) {
	f := newFRFixture(t)
	seedEveryState(f)
	for _, r := range frList(t, f, "aggregatorLFDI="+frAggLFDI).Requests {
		switch r.State {
		case "pending":
			if r.DeadlineAt == nil || *r.DeadlineAt != frNow-10+300 {
				t.Errorf("pending deadlineAt = %v, want %d", r.DeadlineAt, frNow-10+300)
			}
		case "overdue":
			if r.DeadlineAt == nil || *r.DeadlineAt != frNow-400+300 {
				t.Errorf("overdue deadlineAt = %v, want %d", r.DeadlineAt, frNow-400+300)
			}
		default:
			if r.DeadlineAt != nil {
				t.Errorf("%s deadlineAt = %d, want null", r.State, *r.DeadlineAt)
			}
		}
	}
}

func TestFlowReservationStateFilter(t *testing.T) {
	f := newFRFixture(t)
	seedEveryState(f)
	tests := []struct {
		query string
		want  []string
	}{
		{"state=pending", []string{"frq-pending"}},
		{"state=denied,cancelled", []string{"frq-cancelled", "frq-denied"}},
		{"state=ended&state=withdrawn", []string{"frq-ended", "frq-withdrawn"}},
	}
	for _, tc := range tests {
		var ids []string
		for _, r := range frList(t, f, "aggregatorLFDI="+frAggLFDI+"&"+tc.query).Requests {
			ids = append(ids, r.FrqID)
		}
		slices.Sort(ids)
		if !slices.Equal(ids, tc.want) {
			t.Errorf("%s listed %v, want %v", tc.query, ids, tc.want)
		}
	}
}

func TestFlowReservationListOrder(t *testing.T) {
	f := newFRFixture(t)
	win := func(start int64) *sep2.DateTimeInterval { return &sep2.DateTimeInterval{Start: start, Duration: 600} }
	spec := func(id, mrid string, created, start int64) frqSpec {
		return frqSpec{edev: "4", id: id, mrid: mrid, created: created, interval: win(start)}
	}
	// Two pending, the later deadline created first; four answered or not
	// that Table 54 orders by start, then newer creationTime, then larger mRID.
	f.request(spec("frq-pend-late", "P2", frNow-5, frNow+9000))
	f.request(spec("frq-pend-soon", "P1", frNow-20, frNow+9000))
	f.request(frqSpec{edev: "4", id: "frq-a", mrid: "A", created: frNow - 4000, cancelled: true, interval: win(5000)})
	f.request(frqSpec{edev: "4", id: "frq-b", mrid: "B", created: frNow - 4000, cancelled: true, interval: win(1000)})
	f.request(frqSpec{edev: "4", id: "frq-c", mrid: "C", created: frNow - 3000, cancelled: true, interval: win(1000)})
	f.request(frqSpec{edev: "4", id: "frq-d", mrid: "D", created: frNow - 3000, cancelled: true, interval: win(1000)})
	f.request(frqSpec{edev: "4", id: "frq-none", mrid: "Z", created: frNow - 3000, cancelled: true})

	var ids []string
	for _, r := range frList(t, f, "aggregatorLFDI="+frAggLFDI).Requests {
		ids = append(ids, r.FrqID)
	}
	want := []string{"frq-pend-soon", "frq-pend-late", "frq-d", "frq-c", "frq-b", "frq-a", "frq-none"}
	if !slices.Equal(ids, want) {
		t.Errorf("order = %v, want %v", ids, want)
	}
}

func TestFlowReservationFleetMembership(t *testing.T) {
	f := newFRFixture(t)
	f.device("5", frManagedLFDI)
	f.device("6", "C0C0000000000000000000000000000000000003")
	if err := f.pairs.Assign(context.Background(), frAggLFDI, frManagedLFDI); err != nil {
		t.Fatal(err)
	}
	start := &sep2.DateTimeInterval{Start: frNow + 9000, Duration: 600}
	f.request(frqSpec{edev: "4", id: "frq-own", mrid: "O", created: frNow - 5, interval: start})
	f.request(frqSpec{edev: "5", id: "frq-managed", mrid: "G", created: frNow - 5, interval: start})
	f.request(frqSpec{edev: "6", id: "frq-stranger", mrid: "S", created: frNow - 5, interval: start})

	got := frList(t, f, "aggregatorLFDI="+strings.ToLower(frAggLFDI))
	if got.AggregatorLFDI != frAggLFDI {
		t.Errorf("aggregatorLFDI = %q, want the upper-case %q", got.AggregatorLFDI, frAggLFDI)
	}
	ids := []string{}
	for _, r := range got.Requests {
		ids = append(ids, r.EdevID+"/"+r.FrqID)
	}
	slices.Sort(ids)
	if want := []string{"4/frq-own", "5/frq-managed"}; !slices.Equal(ids, want) {
		t.Errorf("fleet requests = %v, want %v (own and managed device, not the stranger's)", ids, want)
	}
}

func TestFlowReservationAbsentQuantitiesAreNull(t *testing.T) {
	f := newFRFixture(t)
	f.request(frqSpec{edev: "4", id: "frq-bare", mrid: "B", created: frNow - 5})
	f.response(frpSpec{edev: "4", id: "frq-bare", mrid: "R-B", subject: "B", created: frNow - 1,
		interval: &sep2.DateTimeInterval{Start: frNow + 60, Duration: 0}})

	w := frGet(t, f.handler().HandleList(), "/api/derms/flow-reservations?aggregatorLFDI="+frAggLFDI, nil)
	var body struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Requests) != 1 {
		t.Fatalf("decode: %v, %d requests\n%s", err, len(body.Requests), w.Body.String())
	}
	req := body.Requests[0]["request"].(map[string]any)
	for _, k := range []string{"intervalRequested", "energyRequested", "powerRequested", "direction"} {
		if v, present := req[k]; !present || v != nil {
			t.Errorf("request.%s = %v (present %v), want null", k, v, present)
		}
	}
	tip := body.Requests[0]["tip"].(map[string]any)
	for _, k := range []string{"energyAvailable", "powerAvailable", "direction", "energyRemainingWh", "cancelReason", "cancelledBy"} {
		if v, present := tip[k]; !present || v != nil {
			t.Errorf("tip.%s = %v (present %v), want null", k, v, present)
		}
	}
	if v := tip["energyCommittedWh"]; v != float64(0) {
		t.Errorf("tip.energyCommittedWh = %v, want 0: a denial executes nothing", v)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"executions": []`)) {
		t.Errorf("a response with no execution must serialize executions as [], got\n%s", w.Body.String())
	}
}

func TestFlowReservationUnrecordedAttribution(t *testing.T) {
	f := newFRFixture(t)
	f.request(frqSpec{edev: "4", id: "frq-x", mrid: "X", created: frNow - 100,
		interval: &sep2.DateTimeInterval{Start: frNow + 60, Duration: 600}, energy: &sep2.SignedRealEnergy{Value: 100}})
	f.response(frpSpec{edev: "4", id: "frq-x", mrid: "R-X", subject: "X", created: frNow - 60,
		interval: &sep2.DateTimeInterval{Start: frNow + 60, Duration: 600}, energy: &sep2.SignedRealEnergy{Value: 100},
		power: &sep2.ActivePower{Value: 50}, cancelledAt: ptr(frNow - 20)})

	h := f.handler()
	h.Attributions = nil
	w := frGet(t, h.HandleGet(), "/api/derms/flow-reservations/4/frq-x", map[string]string{"edevId": "4", "frqId": "frq-x"})
	var v struct {
		Tip struct {
			AnsweredBy struct {
				Kind                 string
				At                   int64
				Admission, Principal *string
			} `json:"answeredBy"`
			CancelledBy *struct {
				Kind string
				At   int64
			} `json:"cancelledBy"`
		} `json:"tip"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	if a := v.Tip.AnsweredBy; a.Kind != "unrecorded" || a.At != frNow-60 || a.Admission != nil || a.Principal != nil {
		t.Errorf("answeredBy = %+v, want unrecorded at the response's creationTime, no admission or principal", a)
	}
	if c := v.Tip.CancelledBy; c == nil || c.Kind != "unrecorded" || c.At != frNow-20 {
		t.Errorf("cancelledBy = %+v, want unrecorded at the cancel time", c)
	}
}

func TestFlowReservationEnergyFigures(t *testing.T) {
	f := newFRFixture(t)
	f.request(frqSpec{edev: "4", id: "frq-e", mrid: "E", created: frNow - 100,
		interval: &sep2.DateTimeInterval{Start: frNow - 50, Duration: 7200}, energy: &sep2.SignedRealEnergy{Value: 10}})
	f.response(frpSpec{edev: "4", id: "frq-e", mrid: "R-E", subject: "E", created: frNow - 60,
		interval: &sep2.DateTimeInterval{Start: frNow - 50, Duration: 7200}, energy: &sep2.SignedRealEnergy{Value: -10, Multiplier: 3},
		power: &sep2.ActivePower{Value: 9000}})
	win := func(dur uint32) sep2.DateTimeInterval { return sep2.DateTimeInterval{Start: frNow - 50, Duration: dur} }
	// 2000 W x 2 devices x 900 s = 1000 Wh, and 1000 W x 1 x 1800 s = 500 Wh;
	// a cancelled execution counts for nothing.
	f.control(ctrlSpec{scope: "7/1/1", id: "1", mrid: "C1", grant: "R-E", created: frNow - 55, window: win(900), target: 2000, reach: 2})
	f.control(ctrlSpec{scope: "7/1/1", id: "2", mrid: "C2", grant: "R-E", created: frNow - 55, window: win(1800), target: 1000, reach: 1})
	f.control(ctrlSpec{scope: "7/1/1", id: "3", mrid: "C3", grant: "R-E", created: frNow - 55, window: win(3600), target: 9000, reach: 1, cancelledAt: ptr(frNow - 40)})

	got := frList(t, f, "aggregatorLFDI="+frAggLFDI).Requests
	if len(got) != 1 || got[0].Tip == nil {
		t.Fatalf("list = %+v, want one answered request", got)
	}
	tip := got[0].Tip
	if tip.EnergyCommittedWh == nil || *tip.EnergyCommittedWh != 1500 {
		t.Errorf("energyCommittedWh = %v, want 1500", tip.EnergyCommittedWh)
	}
	if tip.EnergyRemainingWh == nil || *tip.EnergyRemainingWh != 8500 {
		t.Errorf("energyRemainingWh = %v, want 8500 (10 kWh available less 1500 Wh)", tip.EnergyRemainingWh)
	}

	w := frGet(t, f.handler().HandleGet(), "/x", map[string]string{"edevId": "4", "frqId": "frq-e"})
	var one struct {
		Tip struct {
			Executions []struct {
				MRID        string                  `json:"mRID"`
				Href        string                  `json:"href"`
				ListHref    string                  `json:"derControlListHref"`
				EventStatus struct{ Status string } `json:"eventStatus"`
			} `json:"executions"`
		} `json:"tip"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	var ids, statuses []string
	for _, e := range one.Tip.Executions {
		ids = append(ids, e.MRID)
		statuses = append(statuses, e.EventStatus.Status)
		if want := "/edev/7/fsa/1/derp/1/derc"; e.ListHref != want || !strings.HasPrefix(e.Href, want+"/") {
			t.Errorf("execution %s hrefs = %q, %q, want under %s", e.MRID, e.Href, e.ListHref, want)
		}
	}
	if want := []string{"C1", "C2", "C3"}; !slices.Equal(ids, want) {
		t.Errorf("executions = %v, want %v, the cancelled one included", ids, want)
	}
	if want := []string{"active", "active", "cancelled"}; !slices.Equal(statuses, want) {
		t.Errorf("execution statuses = %v, want %v", statuses, want)
	}
}

func TestFlowReservationGet(t *testing.T) {
	f := newFRFixture(t)
	f.seedGolden()
	h := f.handler()

	w := frGet(t, h.HandleGet(), "/x", map[string]string{"edevId": "4", "frqId": "frq-1789990000000000002"})
	if w.Code != http.StatusOK {
		t.Fatalf("get = %d %s, want 200", w.Code, w.Body.String())
	}
	var v struct {
		FrqID          string                `json:"frqId"`
		AggregatorLFDI string                `json:"aggregatorLFDI"`
		State          string                `json:"state"`
		Responses      []struct{ ID string } `json:"responses"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.FrqID != "frq-1789990000000000002" || v.AggregatorLFDI != frAggLFDI || v.State != "granted" || len(v.Responses) != 2 {
		t.Errorf("view = %+v, want the granted request with a two-response chain under the fleet", v)
	}

	for name, ids := range map[string]map[string]string{
		"unknown request":   {"edevId": "4", "frqId": "frq-nope"},
		"unknown EndDevice": {"edevId": "99", "frqId": "frq-1789990000000000002"},
	} {
		w := frGet(t, h.HandleGet(), "/x", ids)
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if w.Code != http.StatusNotFound || body["code"] != "request_not_found" || body["frqId"] != ids["frqId"] || body["error"] == "" {
			t.Errorf("%s = %d %v, want 404 request_not_found naming the id", name, w.Code, body)
		}
	}
}

func TestFlowReservationGetOfADeviceGoneIsNotFound(t *testing.T) {
	f := newFRFixture(t)
	f.request(frqSpec{edev: "88", id: "frq-orphan", mrid: "O", created: frNow})
	w := frGet(t, f.handler().HandleGet(), "/x", map[string]string{"edevId": "88", "frqId": "frq-orphan"})
	if w.Code != http.StatusNotFound {
		t.Errorf("get of a request under a device no fleet holds = %d, want 404", w.Code)
	}
	if got := frList(t, f, "aggregatorLFDI="+frAggLFDI).Requests; len(got) != 0 {
		t.Errorf("list = %d requests, want the orphan skipped", len(got))
	}
}

func TestFlowReservationGrants(t *testing.T) {
	f := newFRFixture(t)
	seedEveryState(f)
	// A charging grant (positive energy) executes as a negative target; a
	// discharging one as a positive target.
	f.request(frqSpec{edev: "4", id: "frq-dis", mrid: "M-dis", created: frNow - 50,
		interval: &sep2.DateTimeInterval{Start: frNow + 100, Duration: 600}, energy: &sep2.SignedRealEnergy{Value: -4000}})
	f.response(frpSpec{edev: "4", id: "frq-dis", mrid: "R-dis", subject: "M-dis", created: frNow - 40,
		interval: &sep2.DateTimeInterval{Start: frNow + 100, Duration: 600}, energy: &sep2.SignedRealEnergy{Value: -4000},
		power: &sep2.ActivePower{Value: 3, Multiplier: 3}})

	w := frGet(t, f.handler().HandleGrants(), "/api/derms/grants?aggregatorLFDI="+frAggLFDI+"&live=true", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("grants = %d %s, want 200", w.Code, w.Body.String())
	}
	var body struct {
		AggregatorLFDI string `json:"aggregatorLFDI"`
		Now            int64  `json:"now"`
		Grants         []struct {
			FrqID    string                `json:"frqId"`
			Response struct{ MRID string } `json:"response"`
			Target   struct {
				Value      int64
				Multiplier int8
			} `json:"suggestedTargetW"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.AggregatorLFDI != frAggLFDI || body.Now != frNow {
		t.Errorf("list header = %q %d, want the fleet and the pinned clock", body.AggregatorLFDI, body.Now)
	}
	got := map[string]int64{}
	for _, g := range body.Grants {
		got[g.FrqID] = g.Target.Value
		if g.FrqID == "frq-dis" && (g.Target.Multiplier != 3 || g.Response.MRID != "R-dis") {
			t.Errorf("frq-dis grant = %+v, want multiplier 3 and its own response", g)
		}
	}
	want := map[string]int64{"frq-granted": -8000, "frq-dis": 3}
	if len(got) != len(want) || got["frq-granted"] != want["frq-granted"] || got["frq-dis"] != want["frq-dis"] {
		t.Errorf("live grants and targets = %v, want %v (not the denied, cancelled, ended or unanswered)", got, want)
	}
}

func TestFlowReservationGrantsListIsAnArray(t *testing.T) {
	f := newFRFixture(t)
	w := frGet(t, f.handler().HandleGrants(), "/api/derms/grants?aggregatorLFDI="+frAggLFDI+"&live=true", nil)
	if !strings.Contains(w.Body.String(), `"grants": []`) {
		t.Errorf("an empty grant list must serialize as [], got\n%s", w.Body.String())
	}
}

func TestFlowReservationBadQuery(t *testing.T) {
	f := newFRFixture(t)
	h := f.handler()
	tests := []struct {
		name   string
		h      http.HandlerFunc
		target string
		code   string
	}{
		{"missing fleet", h.HandleList(), "/x", "aggregator_lfdi_invalid"},
		{"short fleet", h.HandleList(), "/x?aggregatorLFDI=ABC", "aggregator_lfdi_invalid"},
		{"non-hex fleet", h.HandleList(), "/x?aggregatorLFDI=" + strings.Repeat("G", 40), "aggregator_lfdi_invalid"},
		{"unknown state", h.HandleList(), "/x?aggregatorLFDI=" + frAggLFDI + "&state=pending,bogus", "state_invalid"},
		{"empty state", h.HandleList(), "/x?aggregatorLFDI=" + frAggLFDI + "&state=", "state_invalid"},
		{"grants need a fleet", h.HandleGrants(), "/x?live=true", "aggregator_lfdi_invalid"},
		{"grants need live", h.HandleGrants(), "/x?aggregatorLFDI=" + frAggLFDI, "live_required"},
		{"live must be true", h.HandleGrants(), "/x?aggregatorLFDI=" + frAggLFDI + "&live=false", "live_required"},
	}
	for _, tc := range tests {
		w := frGet(t, tc.h, tc.target, nil)
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: decode: %v", tc.name, err)
		}
		if w.Code != http.StatusBadRequest || body["code"] != tc.code || body["error"] == "" {
			t.Errorf("%s = %d %v, want 400 %s", tc.name, w.Code, body, tc.code)
		}
	}
}

// The wrappers below fail one read, to prove a store error is a 500 and
// never a partial 200.
var errFRStore = errors.New("store unavailable")

type frFailingRequests struct {
	handler.FlowReservationRequestReader
	failList, failParents bool
}

func (r frFailingRequests) List(ctx context.Context, p string, o store.ListOptions) (store.ListResult[sep2.FlowReservationRequest], error) {
	if r.failList {
		return store.ListResult[sep2.FlowReservationRequest]{}, errFRStore
	}
	return r.FlowReservationRequestReader.List(ctx, p, o)
}

func (r frFailingRequests) Parents(ctx context.Context) ([]string, error) {
	if r.failParents {
		return nil, errFRStore
	}
	return r.FlowReservationRequestReader.Parents(ctx)
}

type frFailingResponses struct {
	store.ScopedReader[sep2.FlowReservationResponse]
}

func (frFailingResponses) Get(context.Context, string, string) (sep2.FlowReservationResponse, error) {
	return sep2.FlowReservationResponse{}, errFRStore
}

type frFailingLifecycles struct{}

func (frFailingLifecycles) Get(context.Context, string, string) (dercontrol.LifecycleRecord, error) {
	return dercontrol.LifecycleRecord{}, errFRStore
}

type frFailingExecutions struct{}

func (frFailingExecutions) ExecutionsOf(context.Context, string) ([]commitment.Control, error) {
	return nil, errFRStore
}

type frFailingFleets struct{}

func (frFailingFleets) FleetOf(context.Context, string) (string, error) { return "", errFRStore }

type frFailingControls struct {
	store.ScopedReader[sep2.DERControl]
}

func (frFailingControls) Get(context.Context, string, string) (sep2.DERControl, error) {
	return sep2.DERControl{}, errFRStore
}

func TestFlowReservationStoreErrorIsA500NotAPartialList(t *testing.T) {
	cases := map[string]func(h *handler.AdminFlowReservationHandler){
		"request parents":  func(h *handler.AdminFlowReservationHandler) { h.Requests = frFailingRequests{h.Requests, false, true} },
		"request list":     func(h *handler.AdminFlowReservationHandler) { h.Requests = frFailingRequests{h.Requests, true, false} },
		"response get":     func(h *handler.AdminFlowReservationHandler) { h.Responses = frFailingResponses{h.Responses} },
		"lifecycle get":    func(h *handler.AdminFlowReservationHandler) { h.Lifecycles = frFailingLifecycles{} },
		"executions":       func(h *handler.AdminFlowReservationHandler) { h.Executions = frFailingExecutions{} },
		"served controls":  func(h *handler.AdminFlowReservationHandler) { h.Controls = frFailingControls{h.Controls} },
		"fleet resolution": func(h *handler.AdminFlowReservationHandler) { h.Fleets = frFailingFleets{} },
		"attributions": func(h *handler.AdminFlowReservationHandler) {
			h.Attributions = fakeFRAttributions{err: errFRStore}
		},
	}
	routes := map[string]struct {
		h      func(*handler.AdminFlowReservationHandler) http.HandlerFunc
		target string
		pv     map[string]string
	}{
		"list":   {(*handler.AdminFlowReservationHandler).HandleList, "/x?aggregatorLFDI=" + frAggLFDI, nil},
		"grants": {(*handler.AdminFlowReservationHandler).HandleGrants, "/x?aggregatorLFDI=" + frAggLFDI + "&live=true", nil},
		"get":    {(*handler.AdminFlowReservationHandler).HandleGet, "/x", map[string]string{"edevId": "4", "frqId": "frq-1789990000000000002"}},
	}
	for name, breakIt := range cases {
		for route, r := range routes {
			f := newFRFixture(t)
			f.seedGolden()
			h := f.handler()
			breakIt(h)
			w := frGet(t, r.h(h), r.target, r.pv)
			if name == "request list" && route == "get" || name == "request parents" && route == "get" {
				continue // the single read does not walk the request store
			}
			if w.Code != http.StatusInternalServerError {
				t.Errorf("%s with a failing %s = %d, want 500", route, name, w.Code)
				continue
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s/%s: decode: %v", route, name, err)
			}
			if body["code"] != "internal" || body["requests"] != nil || body["grants"] != nil {
				t.Errorf("%s with a failing %s body = %v, want the internal refusal and no partial list", route, name, body)
			}
			if strings.Contains(w.Body.String(), errFRStore.Error()) {
				t.Errorf("%s with a failing %s leaked the store error text: %s", route, name, w.Body.String())
			}
		}
	}
}

func TestFlowReservationDeadlineFollowsConfig(t *testing.T) {
	f := newFRFixture(t)
	f.request(frqSpec{edev: "4", id: "frq-d", mrid: "D", created: frNow - 100,
		interval: &sep2.DateTimeInterval{Start: frNow + 9000, Duration: 600}})
	h := f.handler()
	h.Deadline = flowreservation.Config{Deadline: 90 * time.Second}

	w := frGet(t, h.HandleList(), "/x?aggregatorLFDI="+frAggLFDI, nil)
	var body struct {
		DeadlineSeconds int `json:"deadlineSeconds"`
		Requests        []struct {
			State      string `json:"state"`
			DeadlineAt *int64 `json:"deadlineAt"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.DeadlineSeconds != 90 {
		t.Errorf("deadlineSeconds = %d, want 90", body.DeadlineSeconds)
	}
	// Created 100 s ago under a 90 s bound: already overdue, due at -10 s.
	if len(body.Requests) != 1 || body.Requests[0].State != "overdue" || *body.Requests[0].DeadlineAt != frNow-10 {
		t.Errorf("requests = %+v, want one overdue with deadlineAt %d", body.Requests, frNow-10)
	}
}
