package handler_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestHandleListFleets_PostThroughRealMupHandlerAttributesToManagedDevice
// closes the open HIGH the security lane found before #720 landed: an
// aggregator posts a mirror reading through the REAL POST /mup handler
// (pkg/sep2srv/handlers/metering.HandleCreateMirrorUsagePoint), naming a
// device it manages, for two managed devices (100 W and 250 W, the review
// lanes' repro). The fleet API must attribute each reading to the named
// device, not the posting aggregator, and the roll-up must sum to 350.
func TestHandleListFleets_PostThroughRealMupHandlerAttributesToManagedDevice(t *testing.T) {
	t.Parallel()
	managers := memory.NewEndDeviceManagementStore()
	if err := managers.Assign(context.Background(), fleetAggregatorLFDI, fleetDeviceALFDI); err != nil {
		t.Fatalf("Assign device A: %v", err)
	}
	if err := managers.Assign(context.Background(), fleetAggregatorLFDI, fleetDeviceBLFDI); err != nil {
		t.Fatalf("Assign device B: %v", err)
	}

	mups := memory.NewStore[sep2.MirrorUsagePoint]()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mup", metering.HandleCreateMirrorUsagePoint(
		mups, managers, aggregatorIdentity(fleetAggregatorLFDI), nil,
	))

	postMirrorAsAggregator(t, mux, fleetDeviceALFDI, "a-mrid", 100)
	postMirrorAsAggregator(t, mux, fleetDeviceBLFDI, "b-mrid", 250)

	fh := &handler.AdminFleetHandler{Managers: managers, MirrorUsagePoints: mups}
	fleets := fetchFleets(t, fh)
	if len(fleets) != 1 {
		t.Fatalf("len(fleets) = %d, want 1", len(fleets))
	}
	fleet := fleets[0]

	devA := findDevice(t, fleet, fleetDeviceALFDI)
	if devA.Measurements.P == nil || devA.Measurements.P.Value != 100 {
		t.Errorf("device A Measurements.P = %+v, want value 100", devA.Measurements.P)
	}
	devB := findDevice(t, fleet, fleetDeviceBLFDI)
	if devB.Measurements.P == nil || devB.Measurements.P.Value != 250 {
		t.Errorf("device B Measurements.P = %+v, want value 250", devB.Measurements.P)
	}
	if fleet.Rollup.P.Sum != 350 {
		t.Errorf("Rollup.P.Sum = %v, want 350", fleet.Rollup.P.Sum)
	}
	if fleet.Rollup.P.Unreported != 1 {
		t.Errorf("Rollup.P.Unreported = %d, want 1: the fleet is the aggregator's own EndDevice plus its managed devices, and the aggregator itself never posted a reading", fleet.Rollup.P.Unreported)
	}
}

// aggregatorIdentity returns a metering.LFDIProvider that always answers
// lfdi, standing in for the poster's certificate identity the way the real
// auth.GetIdentity closure would for that connection.
func aggregatorIdentity(lfdi string) metering.LFDIProvider {
	return func(context.Context) (string, bool) { return lfdi, true }
}

// postMirrorAsAggregator POSTs a MirrorUsagePoint through mux, claiming
// deviceLFDI as the mirrored device (#720's deviceLFDI claim), with one
// inline Reverse-direction (export-positive under the default 2018 edition)
// Watts reading of the given magnitude.
func postMirrorAsAggregator(t *testing.T, mux *http.ServeMux, deviceLFDI, mrid string, watts int64) {
	t.Helper()
	mup := sep2.MirrorUsagePoint{
		MRID:       mrid,
		DeviceLFDI: deviceLFDI,
		MirrorMeterReading: []sep2.MirrorMeterReading{{
			MRID: mrid + "-p",
			ReadingType: &sep2.ReadingType{
				Uom: u8(sep2.UomWatts), FlowDirection: u8(sep2.FlowDirectionReverse), PowerOfTenMultiplier: i8(0),
			},
			Reading: &sep2.Reading{Value: i64(watts)},
		}},
	}
	body, err := xml.Marshal(&mup)
	if err != nil {
		t.Fatalf("marshal MirrorUsagePoint for %q: %v", deviceLFDI, err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mup", bytes.NewReader(body)))
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /mup for %q: status = %d, want 201; body = %s", deviceLFDI, w.Code, w.Body.String())
	}
}
