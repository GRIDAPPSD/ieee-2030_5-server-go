package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
)

// Fix round 1 for #764.

func frListAt(t *testing.T, f *frFixture, now int64, query string) FRListBody {
	t.Helper()
	h := f.handler()
	h.Now = func() int64 { return now }
	w := frGet(t, h.HandleList(), "/x?aggregatorLFDI="+frAggLFDI+query, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list = %d %s, want 200", w.Code, w.Body.String())
	}
	var out FRListBody
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func frGrantIDs(t *testing.T, f *frFixture, now int64) []string {
	t.Helper()
	h := f.handler()
	h.Now = func() int64 { return now }
	w := frGet(t, h.HandleGrants(), "/x?aggregatorLFDI="+frAggLFDI+"&live=true", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("grants = %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Grants []struct {
			FrqID string `json:"frqId"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, g := range body.Grants {
		ids = append(ids, g.FrqID)
	}
	slices.Sort(ids)
	return ids
}

func win(start int64, dur uint32) *sep2.DateTimeInterval {
	return &sep2.DateTimeInterval{Start: start, Duration: dur}
}

// A client cancel marks the request before it cancels the grant. When the
// second step fails the grant keeps running, so the view must still show it.
func TestFlowReservationCancelledRequestWithLiveTipStaysGranted(t *testing.T) {
	f := newFRFixture(t)
	energy := &sep2.SignedRealEnergy{Value: 4000}
	power := &sep2.ActivePower{Value: 8000}
	add := func(id string, start int64, dur uint32, cancelledAt *int64) {
		f.request(frqSpec{edev: "4", id: id, mrid: "M-" + id, created: frNow - 100, cancelled: true, interval: win(start, 3600), energy: energy, power: power})
		f.response(frpSpec{edev: "4", id: id, mrid: "R-" + id, subject: "M-" + id, created: frNow - 90, interval: win(start, dur), energy: energy, power: power, cancelledAt: cancelledAt})
	}
	add("frq-live", frNow-50, 3600, nil)           // cancel did not finish
	add("frq-done", frNow-50, 3600, ptr(frNow-10)) // cancel finished
	add("frq-denied", frNow-50, 0, nil)            // a denial
	add("frq-over", frNow-9000, 600, nil)          // grant already ended
	f.request(frqSpec{edev: "4", id: "frq-open", mrid: "M-open", created: frNow - 10, cancelled: true, interval: win(frNow+9000, 60)})

	var raw struct {
		Requests []struct {
			FrqID            string `json:"frqId"`
			State            string `json:"state"`
			RequestCancelled *bool  `json:"requestCancelled"`
		} `json:"requests"`
	}
	h := f.handler()
	w := frGet(t, h.HandleList(), "/x?aggregatorLFDI="+frAggLFDI, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"frq-live": "granted", "frq-done": "withdrawn", "frq-denied": "withdrawn", "frq-over": "withdrawn", "frq-open": "withdrawn"}
	for _, r := range raw.Requests {
		if r.State != want[r.FrqID] {
			t.Errorf("%s state = %q, want %q", r.FrqID, r.State, want[r.FrqID])
		}
		if live := r.FrqID == "frq-live"; live != (r.RequestCancelled != nil && *r.RequestCancelled) {
			t.Errorf("%s requestCancelled = %v, want %v", r.FrqID, r.RequestCancelled, live)
		}
		if r.FrqID != "frq-live" && r.RequestCancelled != nil {
			t.Errorf("%s carries requestCancelled, want it absent", r.FrqID)
		}
	}
	if got := frGrantIDs(t, f, frNow); !slices.Equal(got, []string{"frq-live"}) {
		t.Errorf("grants = %v, want the still-live grant listed", got)
	}

	one := frGet(t, h.HandleGet(), "/x", map[string]string{"edevId": "4", "frqId": "frq-live"})
	if !strings.Contains(one.Body.String(), `"requestCancelled": true`) || !strings.Contains(one.Body.String(), `"state": "granted"`) {
		t.Errorf("single read lost the incomplete cancel:\n%s", one.Body.String())
	}
}

func TestFlowReservationStateBoundaries(t *testing.T) {
	f := newFRFixture(t)
	f.request(frqSpec{edev: "4", id: "frq-wait", mrid: "W", created: 1000, interval: win(9000, 60)}) // deadline 1300
	f.request(frqSpec{edev: "4", id: "frq-run", mrid: "R", created: 1000, interval: win(2000, 100), energy: &sep2.SignedRealEnergy{Value: 10}})
	f.response(frpSpec{edev: "4", id: "frq-run", mrid: "R-run", subject: "R", created: 1100, interval: win(2000, 100),
		energy: &sep2.SignedRealEnergy{Value: 10}, power: &sep2.ActivePower{Value: 10}}) // ends at 2100
	for _, tc := range []struct {
		now         int64
		wait, run   string
		runIsListed bool
	}{
		{1299, "pending", "granted", true},
		{1300, "overdue", "granted", true},
		{2099, "overdue", "granted", true},
		{2100, "overdue", "ended", false},
	} {
		got := frListAt(t, f, tc.now, "").states()
		if got["frq-wait"] != tc.wait || got["frq-run"] != tc.run {
			t.Errorf("now=%d states = %v, want wait=%s run=%s", tc.now, got, tc.wait, tc.run)
		}
		listed := slices.Contains(frGrantIDs(t, f, tc.now), "frq-run")
		if listed != tc.runIsListed {
			t.Errorf("now=%d grants lists frq-run = %v, want %v", tc.now, listed, tc.runIsListed)
		}
	}
}

func TestFlowReservationQuantityBranches(t *testing.T) {
	f := newFRFixture(t)
	add := func(id string, energy int64, mult int8, power *sep2.ActivePower, withInterval bool) {
		f.request(frqSpec{edev: "4", id: id, mrid: "M-" + id, created: frNow - 100, interval: win(frNow+50, 1800)})
		var iv *sep2.DateTimeInterval
		if withInterval {
			iv = win(frNow+50, 1800)
		}
		f.response(frpSpec{edev: "4", id: id, mrid: "R-" + id, subject: "M-" + id, created: frNow - 90, interval: iv,
			energy: &sep2.SignedRealEnergy{Value: energy, Multiplier: mult}, power: power})
	}
	// 25 x 10^-1 = 2.5 Wh available; one execution of 1 W for 1800 s is 0.5 Wh.
	add("frq-neg-mult", 25, -1, &sep2.ActivePower{Value: 100}, true)
	f.control(ctrlSpec{scope: "7/1/1", id: "1", mrid: "C1", grant: "R-frq-neg-mult", created: frNow - 80, window: *win(frNow+50, 1800), target: -1, reach: 1})
	add("frq-charge-negpower", 100, 0, &sep2.ActivePower{Value: -3000}, true)
	add("frq-discharge-negpower", -100, 0, &sep2.ActivePower{Value: -3000}, true)
	add("frq-nopower", 100, 0, nil, true)

	h := f.handler()
	w := frGet(t, h.HandleList(), "/x?aggregatorLFDI="+frAggLFDI, nil)
	var list struct {
		Requests []struct {
			FrqID string `json:"frqId"`
			Tip   struct {
				Committed *float64 `json:"energyCommittedWh"`
				Remaining *float64 `json:"energyRemainingWh"`
			} `json:"tip"`
		} `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, r := range list.Requests {
		if r.FrqID == "frq-neg-mult" {
			if r.Tip.Committed == nil || *r.Tip.Committed != 0.5 || r.Tip.Remaining == nil || *r.Tip.Remaining != 2 {
				t.Errorf("negative-multiplier grant committed/remaining = %v/%v, want 0.5/2", r.Tip.Committed, r.Tip.Remaining)
			}
		}
	}

	gw := frGet(t, h.HandleGrants(), "/x?aggregatorLFDI="+frAggLFDI+"&live=true", nil)
	var g struct {
		Grants []struct {
			FrqID  string                `json:"frqId"`
			Target struct{ Value int64 } `json:"suggestedTargetW"`
		} `json:"grants"`
	}
	if err := json.Unmarshal(gw.Body.Bytes(), &g); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, x := range g.Grants {
		got[x.FrqID] = x.Target.Value
	}
	// A charging grant executes as a negative target and a discharging one as
	// a positive target, whatever sign the stored power carries.
	if got["frq-charge-negpower"] != -3000 || got["frq-discharge-negpower"] != 3000 {
		t.Errorf("targets = %v, want charge -3000 and discharge 3000", got)
	}
	if _, listed := got["frq-nopower"]; listed {
		t.Error("a grant with no powerAvailable is listed, want it left out: it has no target")
	}
}

func TestFlowReservationNilFieldsOfAResponse(t *testing.T) {
	f := newFRFixture(t)
	f.request(frqSpec{edev: "4", id: "frq-nointerval", mrid: "A", created: frNow - 100, interval: win(frNow+50, 600)})
	f.response(frpSpec{edev: "4", id: "frq-nointerval", mrid: "R-A", subject: "A", created: frNow - 90,
		energy: &sep2.SignedRealEnergy{Value: 10}, power: &sep2.ActivePower{Value: 10}})
	f.request(frqSpec{edev: "4", id: "frq-noenergy", mrid: "B", created: frNow - 100, interval: win(frNow+50, 600)})
	f.response(frpSpec{edev: "4", id: "frq-noenergy", mrid: "R-B", subject: "B", created: frNow - 90, interval: win(frNow+50, 600),
		power: &sep2.ActivePower{Value: 10}})
	f.request(frqSpec{edev: "4", id: "frq-zero", mrid: "Z", created: frNow - 100, interval: win(frNow+50, 600), energy: &sep2.SignedRealEnergy{}})

	w := frGet(t, f.handler().HandleList(), "/x?aggregatorLFDI="+frAggLFDI, nil)
	var list struct {
		Requests []map[string]any `json:"requests"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	by := map[string]map[string]any{}
	for _, r := range list.Requests {
		by[r["frqId"].(string)] = r
	}
	if by["frq-nointerval"]["state"] != "denied" {
		t.Errorf("a tip with no interval is %v, want denied", by["frq-nointerval"]["state"])
	}
	tip := by["frq-noenergy"]["tip"].(map[string]any)
	if by["frq-noenergy"]["state"] != "granted" || tip["energyRemainingWh"] != nil || tip["direction"] != nil {
		t.Errorf("a grant with no energyAvailable: state %v remaining %v direction %v, want granted and null", by["frq-noenergy"]["state"], tip["energyRemainingWh"], tip["direction"])
	}
	if d, ok := by["frq-zero"]["request"].(map[string]any)["direction"]; !ok || d != nil {
		t.Errorf("zero requested energy direction = %v (present %v), want null", d, ok)
	}
	if slices.Contains(frGrantIDs(t, f, frNow), "frq-noenergy") {
		t.Error("a grant with no energy has no direction, so it has no target and is not listed")
	}
}

func TestFlowReservationCancelledBeatsEnded(t *testing.T) {
	f := newFRFixture(t)
	f.request(frqSpec{edev: "4", id: "frq-x", mrid: "X", created: 1000, interval: win(2000, 100)})
	f.response(frpSpec{edev: "4", id: "frq-x", mrid: "R-X", subject: "X", created: 1100, interval: win(2000, 100),
		energy: &sep2.SignedRealEnergy{Value: 10}, power: &sep2.ActivePower{Value: 10}, cancelledAt: ptr(int64(2050))})
	if got := frListAt(t, f, 5000, "").states()["frq-x"]; got != "cancelled" {
		t.Errorf("a cancelled grant past its end = %q, want cancelled", got)
	}
}

func TestFlowReservationMalformedRequestHrefIsA500(t *testing.T) {
	f := newFRFixture(t)
	f.request(frqSpec{edev: "4", id: "frq-good", mrid: "G", created: frNow - 5})
	if err := f.frqs.Create(context.Background(), "4", "frq-bad", sep2.FlowReservationRequest{
		Resource: sep2.Resource{Href: "/edev/9/frq/frq-bad"}, MRID: "B", CreationTime: frNow,
	}); err != nil {
		t.Fatal(err)
	}
	w := frGet(t, f.handler().HandleList(), "/x?aggregatorLFDI="+frAggLFDI, nil)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "frq-good") {
		t.Errorf("list with a request whose href names another device = %d %s, want 500 and no partial list", w.Code, w.Body.String())
	}
}

// A device that is gone, or has no LFDI, belongs to no fleet: its requests
// are skipped with a warning that names it, and every fleet still lists.
func TestFlowReservationSkippedDeviceIsLoggedNotFatal(t *testing.T) {
	f := newFRFixture(t)
	f.device("8", "")
	f.request(frqSpec{edev: "4", id: "frq-own", mrid: "O", created: frNow - 5})
	f.request(frqSpec{edev: "8", id: "frq-nolfdi", mrid: "N", created: frNow - 5})
	f.request(frqSpec{edev: "77", id: "frq-gone", mrid: "G", created: frNow - 5})

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	got := frList(t, f, "aggregatorLFDI="+frAggLFDI)
	if len(got.Requests) != 1 || got.Requests[0].FrqID != "frq-own" {
		t.Fatalf("requests = %+v, want only the fleet's own", got.Requests)
	}
	logged := buf.String()
	for _, edev := range []string{"EndDevice 8 ", "EndDevice 77 "} {
		if !strings.Contains(logged, "WARNING") || !strings.Contains(logged, edev) {
			t.Errorf("log %q does not warn about %q", logged, edev)
		}
	}
}

func frAllStatesFixture(t *testing.T) *frFixture {
	f := newFRFixture(t)
	seedEveryState(f)
	// A granted request whose client cancel did not finish.
	f.request(frqSpec{edev: "4", id: "frq-cancelling", mrid: "M-cancelling", created: frNow - 50, cancelled: true,
		interval: win(frNow+100, 3600), energy: &sep2.SignedRealEnergy{Value: 4000}, power: &sep2.ActivePower{Value: 8000}})
	f.response(frpSpec{edev: "4", id: "frq-cancelling", mrid: "R-cancelling", subject: "M-cancelling", created: frNow - 40,
		interval: win(frNow+100, 3600), energy: &sep2.SignedRealEnergy{Value: 4000}, power: &sep2.ActivePower{Value: 8000}})
	f.attribute("4", "frq-granted", &flowreservation.Attribution{Kind: "operator", Admission: "mtls", Principal: frCert, At: frNow - 60}, nil)
	return f
}

func TestFlowReservationEveryStateMatchesItsGolden(t *testing.T) {
	f := frAllStatesFixture(t)
	h := f.handler()
	w := frGet(t, h.HandleList(), "/x?aggregatorLFDI="+frAggLFDI, nil)
	golden, err := os.ReadFile("testdata/flowreservation_states.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(w.Body.Bytes(), golden) {
		t.Errorf("list body differs from testdata/flowreservation_states.golden.json\n%s", w.Body.String())
	}
	var list FRListBody
	if err := json.Unmarshal(golden, &list); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range list.Requests {
		seen[r.State] = true
	}
	for _, s := range []string{"pending", "overdue", "granted", "denied", "cancelled", "withdrawn", "ended"} {
		if !seen[s] {
			t.Errorf("the states golden holds no %s request", s)
		}
	}
}
