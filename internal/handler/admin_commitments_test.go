package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

type commitmentsFunc func(ctx context.Context, fleetKey string, now int64) (commitment.Commitments, error)

func (f commitmentsFunc) Commitments(ctx context.Context, fleetKey string, now int64) (commitment.Commitments, error) {
	return f(ctx, fleetKey, now)
}

type knownFunc func(ctx context.Context, fleetKey string) (bool, error)

func (f knownFunc) Known(ctx context.Context, fleetKey string) (bool, error) { return f(ctx, fleetKey) }

var everyFleetKnown = knownFunc(func(context.Context, string) (bool, error) { return true, nil })

// A failed read is a 500 naming no commitment, never a 200 with empty
// lists: an empty list says the fleet is free.
func TestAdminCommitmentsStoreErrorIs500(t *testing.T) {
	t.Parallel()
	h := &handler.AdminCommitmentsHandler{
		Ledger: commitmentsFunc(func(context.Context, string, int64) (commitment.Commitments, error) {
			return commitment.Commitments{}, errors.New("store unreachable")
		}),
		Fleets: everyFleetKnown,
		Now:    func() int64 { return frNow },
	}
	w := httptest.NewRecorder()
	h.HandleList()(w, httptest.NewRequest(http.MethodGet, "/api/derms/commitments?aggregatorLFDI="+frAggLFDI, nil))
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	if w.Code != http.StatusInternalServerError || string(body["code"]) != `"internal"` || string(body["error"]) != `"commitments read failed, see server log"` {
		t.Errorf("status %d code %s error %s, want 500 internal naming the commitments read", w.Code, body["code"], body["error"])
	}
	if _, ok := body["grants"]; ok {
		t.Errorf("a failed read served grants: %s", w.Body.String())
	}
}

// The fleet key is the aggregator LFDI, canonicalised before the ledger is
// asked, and the clock the handler is given is the one the ledger filters by.
func TestAdminCommitmentsAsksTheLedgerForTheFleet(t *testing.T) {
	t.Parallel()
	var gotFleet string
	var gotNow int64
	h := &handler.AdminCommitmentsHandler{
		Ledger: commitmentsFunc(func(_ context.Context, fleet string, now int64) (commitment.Commitments, error) {
			gotFleet, gotNow = fleet, now
			return commitment.Commitments{}, nil
		}),
		Fleets: everyFleetKnown,
		Now:    func() int64 { return frNow },
	}
	w := httptest.NewRecorder()
	h.HandleList()(w, httptest.NewRequest(http.MethodGet, "/api/derms/commitments?aggregatorLFDI=3e4f45ab31edfe5b67e343e5e4562e31984e23e5", nil))
	if w.Code != http.StatusOK || gotFleet != frAggLFDI || gotNow != frNow {
		t.Fatalf("status %d, ledger asked for %q at %d; want 200, %q at %d", w.Code, gotFleet, gotNow, frAggLFDI, frNow)
	}

	for _, q := range []string{"", "?aggregatorLFDI=xyz"} {
		gotFleet = ""
		w := httptest.NewRecorder()
		h.HandleList()(w, httptest.NewRequest(http.MethodGet, "/api/derms/commitments"+q, nil))
		if w.Code != http.StatusBadRequest || gotFleet != "" {
			t.Errorf("query %q: status %d, ledger asked for %q; want 400 and no read", q, w.Code, gotFleet)
		}
	}
}

// An unknown fleet is a 404 and never reaches the ledger, and a failed
// fleet lookup is a 500.
func TestAdminCommitmentsResolvesTheFleetFirst(t *testing.T) {
	t.Parallel()
	asked := 0
	ledger := commitmentsFunc(func(context.Context, string, int64) (commitment.Commitments, error) {
		asked++
		return commitment.Commitments{}, nil
	})
	cases := []struct {
		name   string
		fleets knownFunc
		status int
		code   string
	}{
		{"unknown", func(context.Context, string) (bool, error) { return false, nil }, http.StatusNotFound, `"fleet_not_found"`},
		{"lookup failed", func(context.Context, string) (bool, error) { return false, errors.New("store unreachable") }, http.StatusInternalServerError, `"internal"`},
	}
	for _, tc := range cases {
		h := &handler.AdminCommitmentsHandler{Ledger: ledger, Fleets: tc.fleets, Now: func() int64 { return frNow }}
		w := httptest.NewRecorder()
		h.HandleList()(w, httptest.NewRequest(http.MethodGet, "/api/derms/commitments?aggregatorLFDI="+frAggLFDI, nil))
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: decode %v\n%s", tc.name, err, w.Body.String())
		}
		if w.Code != tc.status || string(body["code"]) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", tc.name, w.Code, body["code"], tc.status, tc.code)
		}
	}
	if asked != 0 {
		t.Errorf("the ledger was asked %d times, want 0", asked)
	}
}

// The view serves signs and powers of ten as the route documents them: a
// discharge grant has positive power and energy magnitudes with direction
// "discharge", targetW keeps its stored sign, multipliers scale, and a
// grant with no energy serves every energy figure and its direction null.
func TestAdminCommitmentsViewValues(t *testing.T) {
	t.Parallel()
	discharge := commitment.Grant{
		MRID: "G-DIS", ID: "frq-9-r2", EndDeviceID: "agg",
		Window: &commitment.Window{Start: frNow + 100, Duration: 600},
		Energy: &sep2.SignedRealEnergy{Value: -15, Multiplier: 2}, // 1500 Wh discharging
		Power:  &sep2.ActivePower{Value: -25, Multiplier: 1},      // 250 W
	}
	execution := commitment.Control{MRID: "E1", Window: commitment.Window{Start: frNow + 100, Duration: 3600},
		TargetW: &sep2.ActivePower{Value: 3, Multiplier: 2}, Reach: 2} // 300 W x 2 x 1 h = 600 Wh
	noEnergy := commitment.Grant{
		MRID: "G-NOE", ID: "frq-10", EndDeviceID: "agg",
		Window: &commitment.Window{Start: frNow + 900, Duration: 600},
	}
	plain := commitment.Control{MRID: "P1", EndDeviceID: "m1", Window: commitment.Window{Start: frNow, Duration: 60},
		TargetW: &sep2.ActivePower{Value: -125, Multiplier: -1}} // -12.5 W
	h := &handler.AdminCommitmentsHandler{
		Ledger: commitmentsFunc(func(context.Context, string, int64) (commitment.Commitments, error) {
			return commitment.Commitments{
				Grants: []commitment.CommittedGrant{{Grant: noEnergy}, {Grant: discharge, Executions: []commitment.Control{execution}}},
				Plain:  []commitment.Control{plain},
			}, nil
		}),
		Fleets: everyFleetKnown,
		Now:    func() int64 { return frNow },
	}
	w := httptest.NewRecorder()
	h.HandleList()(w, httptest.NewRequest(http.MethodGet, "/api/derms/commitments?aggregatorLFDI="+frAggLFDI, nil))
	var got handler.CommitmentsView
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != http.StatusOK || len(got.Grants) != 2 || len(got.PlainControls) != 1 {
		t.Fatalf("GET = %d %v\n%s", w.Code, err, w.Body.String())
	}

	d := got.Grants[0]
	if d.MRID != "G-DIS" || d.FrqID != "frq-9" || d.Direction == nil || *d.Direction != "discharge" {
		t.Errorf("discharge grant = %s %s %v, want G-DIS frq-9 discharge", d.MRID, d.FrqID, d.Direction)
	}
	if d.PowerW == nil || *d.PowerW != 250 || d.EnergyWh == nil || *d.EnergyWh != 1500 || d.EnergyRemainingWh == nil || *d.EnergyRemainingWh != 900 {
		t.Errorf("discharge grant powerW %v energyWh %v remaining %v, want 250, 1500, 900", d.PowerW, d.EnergyWh, d.EnergyRemainingWh)
	}

	n := got.Grants[1]
	if n.MRID != "G-NOE" || n.Direction != nil || n.EnergyWh != nil || n.EnergyRemainingWh != nil || n.PowerW != nil {
		t.Errorf("grant without energy = %+v, want direction, powerW, energyWh and energyRemainingWh all null", n)
	}
	var raw struct{ Grants []map[string]json.RawMessage }
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"direction", "powerW", "energyWh", "energyRemainingWh"} {
		if v, ok := raw.Grants[1][k]; !ok || string(v) != "null" {
			t.Errorf("grant without energy: %s = %s (present %v), want null", k, v, ok)
		}
	}

	p := got.PlainControls[0]
	if p.MRID != "P1" || p.EdevID != "m1" || p.TargetW == nil || *p.TargetW != -12.5 {
		t.Errorf("plain control = %s %s %v, want P1 m1 -12.5", p.MRID, p.EdevID, p.TargetW)
	}
}
