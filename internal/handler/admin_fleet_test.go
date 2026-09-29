package handler_test

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #715: admin read API acceptance criteria, one test group per criterion.

const (
	fleetAggregatorLFDI = "AAAA000000000000000000000000000000000001"
	fleetDeviceALFDI    = "D001000000000000000000000000000000000001"
	fleetDeviceBLFDI    = "D002000000000000000000000000000000000002"
	fleetOutsideLFDI    = "E001000000000000000000000000000000000001"
	fleetOtherAggLFDI   = "BBBB000000000000000000000000000000000002"
)

// fleetFixture wires every store AdminFleetHandler reads, so a test builds
// only the records its criterion needs.
type fleetFixture struct {
	t                 *testing.T
	managers          *memory.EndDeviceManagementStore
	endDevices        *memory.EndDeviceStore
	ders              *memory.ScopedStore[sep2.DER]
	derStatuses       *memory.ScopedStore[sep2.DERStatus]
	derAvailabilities *memory.ScopedStore[sep2.DERAvailability]
	mups              *memory.Store[sep2.MirrorUsagePoint]
	mmrs              *memory.ScopedStore[sep2.MirrorMeterReading]
}

func newFleetFixture(t *testing.T) *fleetFixture {
	t.Helper()
	return &fleetFixture{
		t:                 t,
		managers:          memory.NewEndDeviceManagementStore(),
		endDevices:        memory.NewEndDeviceStore(),
		ders:              memory.NewScopedStore[sep2.DER](),
		derStatuses:       memory.NewScopedStore[sep2.DERStatus](),
		derAvailabilities: memory.NewScopedStore[sep2.DERAvailability](),
		mups:              memory.NewStore[sep2.MirrorUsagePoint](),
		mmrs:              memory.NewScopedStore[sep2.MirrorMeterReading](),
	}
}

func (f *fleetFixture) handler() *handler.AdminFleetHandler {
	return &handler.AdminFleetHandler{
		Managers:            f.managers,
		EndDevices:          f.endDevices,
		DERs:                f.ders,
		DERStatuses:         f.derStatuses,
		DERAvailabilities:   f.derAvailabilities,
		MirrorUsagePoints:   f.mups,
		MirrorMeterReadings: f.mmrs,
	}
}

func (f *fleetFixture) assign(manager, managed string) {
	f.t.Helper()
	if err := f.managers.Assign(context.Background(), manager, managed); err != nil {
		f.t.Fatalf("Assign(%q, %q): %v", manager, managed, err)
	}
}

// seedDevice registers an EndDevice under edevID and one DER under derID, so
// deviceLFDI resolves to a DER status/availability scope key.
func (f *fleetFixture) seedDevice(edevID, deviceLFDI, derID string) {
	f.t.Helper()
	dev := sep2.EndDevice{
		SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/" + edevID}},
		LFDI:                 deviceLFDI,
	}
	if err := f.endDevices.Create(context.Background(), edevID, dev); err != nil {
		f.t.Fatalf("EndDevices.Create(%q): %v", edevID, err)
	}
	der := sep2.DER{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + edevID + "/der/" + derID},
		},
	}
	if err := f.ders.Create(context.Background(), edevID, derID, der); err != nil {
		f.t.Fatalf("DERs.Create(%q, %q): %v", edevID, derID, err)
	}
}

func (f *fleetFixture) setStatus(edevID, derID string, status sep2.DERStatus) {
	f.t.Helper()
	if err := f.derStatuses.Create(context.Background(), edevID+"/"+derID, "default", status); err != nil {
		f.t.Fatalf("DERStatuses.Create: %v", err)
	}
}

func (f *fleetFixture) setAvailability(edevID, derID string, avail sep2.DERAvailability) {
	f.t.Helper()
	if err := f.derAvailabilities.Create(context.Background(), edevID+"/"+derID, "default", avail); err != nil {
		f.t.Fatalf("DERAvailabilities.Create: %v", err)
	}
}

// postInlineReading creates a MirrorUsagePoint whose single inline
// MirrorMeterReading carries one quantity, the shape a POST /mup body
// stores.
func (f *fleetFixture) postInlineReading(mupID, deviceLFDI string, mmr sep2.MirrorMeterReading) {
	f.t.Helper()
	mup := sep2.MirrorUsagePoint{
		Resource:           sep2.Resource{Href: "/mup/" + mupID},
		DeviceLFDI:         deviceLFDI,
		MirrorMeterReading: []sep2.MirrorMeterReading{mmr},
	}
	if err := f.mups.Create(context.Background(), mupID, mup); err != nil {
		f.t.Fatalf("MirrorUsagePoints.Create(%q): %v", mupID, err)
	}
}

// postOutOfBandReading registers mupID (with no inline reading, matching a
// bare POST /mup) and stores one reading in the separate mmrs collection
// POST /mup/{id}/mr writes to.
func (f *fleetFixture) postOutOfBandReading(mupID, readingID, deviceLFDI string, mmr sep2.MirrorMeterReading) {
	f.t.Helper()
	if _, err := f.mups.Get(context.Background(), mupID); err != nil {
		mup := sep2.MirrorUsagePoint{Resource: sep2.Resource{Href: "/mup/" + mupID}, DeviceLFDI: deviceLFDI}
		if err := f.mups.Create(context.Background(), mupID, mup); err != nil {
			f.t.Fatalf("MirrorUsagePoints.Create(%q): %v", mupID, err)
		}
	}
	if err := f.mmrs.Create(context.Background(), mupID, readingID, mmr); err != nil {
		f.t.Fatalf("MirrorMeterReadings.Create(%q, %q): %v", mupID, readingID, err)
	}
}

func fetchFleets(t *testing.T, h *handler.AdminFleetHandler) []handler.Fleet {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/derms/fleets", nil)
	handler.HandleListFleets(h)(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	var fleets []handler.Fleet
	if err := json.Unmarshal(w.Body.Bytes(), &fleets); err != nil {
		t.Fatalf("decode response: %v; body = %s", err, w.Body.String())
	}
	return fleets
}

func findDevice(t *testing.T, fleet handler.Fleet, lfdi string) handler.FleetDevice {
	t.Helper()
	for _, d := range fleet.Devices {
		if d.LFDI == lfdi {
			return d
		}
	}
	t.Fatalf("fleet %q has no device %q; devices = %+v", fleet.AggregatorLFDI, lfdi, fleet.Devices)
	return handler.FleetDevice{}
}

func hexBin32(v uint32) *sep2.HexBinary32 { h := sep2.HexBinary32(v); return &h }
func u8(v uint8) *uint8                   { return &v }
func u16(v uint16) *uint16                { return &v }
func i8(v int8) *int8                     { return &v }
func i64(v int64) *int64                  { return &v }

// --- AC1: DERStatus (connection, operational state, alarm, state of charge), ---
// --- DERAvailability and mirror readings for P, Q, V, f, each with reading time ---
// --- and quality flags -------------------------------------------------------

func TestHandleListFleets_DeviceStatusAndAvailability(t *testing.T) {
	t.Parallel()
	f := newFleetFixture(t)
	f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)
	f.seedDevice("3", fleetDeviceALFDI, "1")
	f.setStatus("3", "1", sep2.DERStatus{
		GenConnectStatus:      &sep2.ConnectStatusType{DateTime: 1000, Value: 1}, // bit 0 set: connected
		OperationalModeStatus: &sep2.OperationalModeStatusType{DateTime: 1000, Value: 2},
		AlarmStatus:           hexBin32(0x04),
		StateOfChargeStatus:   &sep2.StateOfChargeStatusType{DateTime: 1000, Value: 7500},
		ReadingTime:           1000,
	})
	f.setAvailability("3", "1", sep2.DERAvailability{
		ReadingTime:  1000,
		StatWAvail:   &sep2.ActivePower{Multiplier: 3, Value: 5},   // 5000 W
		StatVarAvail: &sep2.ReactivePower{Multiplier: 3, Value: 2}, // 2000 var
	})

	fleets := fetchFleets(t, f.handler())
	if len(fleets) != 1 {
		t.Fatalf("len(fleets) = %d, want 1", len(fleets))
	}
	dev := findDevice(t, fleets[0], fleetDeviceALFDI)

	if dev.Status == nil {
		t.Fatal("Status is nil, want populated")
	}
	if dev.Status.Connected == nil || !*dev.Status.Connected {
		t.Errorf("Status.Connected = %v, want true", dev.Status.Connected)
	}
	if dev.Status.OperationalMode == nil || *dev.Status.OperationalMode != 2 {
		t.Errorf("Status.OperationalMode = %v, want 2", dev.Status.OperationalMode)
	}
	if dev.Status.AlarmStatus == nil || *dev.Status.AlarmStatus != 0x04 {
		t.Errorf("Status.AlarmStatus = %v, want 0x04", dev.Status.AlarmStatus)
	}
	if dev.Status.StateOfCharge == nil || *dev.Status.StateOfCharge != 7500 {
		t.Errorf("Status.StateOfCharge = %v, want 7500", dev.Status.StateOfCharge)
	}
	if dev.Status.ReadingTime != 1000 {
		t.Errorf("Status.ReadingTime = %d, want 1000", dev.Status.ReadingTime)
	}

	if dev.Availability == nil {
		t.Fatal("Availability is nil, want populated")
	}
	if dev.Availability.StatWAvail == nil || *dev.Availability.StatWAvail != 5000 {
		t.Errorf("Availability.StatWAvail = %v, want 5000", dev.Availability.StatWAvail)
	}
	if dev.Availability.StatVarAvail == nil || *dev.Availability.StatVarAvail != 2000 {
		t.Errorf("Availability.StatVarAvail = %v, want 2000", dev.Availability.StatVarAvail)
	}
}

// --- AC3: readings mapped to export-positive by flow direction, forward and ---
// --- reverse, plus V and f reported unsigned, each with reading time and -----
// --- quality flags -------------------------------------------------------

func TestHandleListFleets_MeasurementsExportPositiveSignConvention(t *testing.T) {
	t.Parallel()
	f := newFleetFixture(t)
	f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)
	// No EndDevice/DER record for this device: the AC's trap is that a
	// reading is attributed by the mirror's own deviceLFDI, never by
	// whether the EndDevice store happens to know the device.

	// P: FlowDirectionForward ("delivered to customer") = import = negative
	// under the export-positive convention.
	f.postInlineReading("mup-p-forward", fleetDeviceALFDI, sep2.MirrorMeterReading{
		MRID:           "p-fwd",
		LastUpdateTime: 100,
		ReadingType: &sep2.ReadingType{
			Uom: u8(sep2.UomWatts), FlowDirection: u8(sep2.FlowDirectionForward), PowerOfTenMultiplier: i8(0),
		},
		Reading: &sep2.Reading{Value: i64(500), QualityFlags: hexBin16(0x01)},
	})
	// Q: FlowDirectionReverse ("received from customer") = export = already
	// export-positive, unchanged.
	f.postOutOfBandReading("mup-q-reverse", "r1", fleetDeviceALFDI, sep2.MirrorMeterReading{
		MRID:           "q-rev",
		LastUpdateTime: 100,
		ReadingType: &sep2.ReadingType{
			Uom: u8(sep2.UomVars), FlowDirection: u8(sep2.FlowDirectionReverse), PowerOfTenMultiplier: i8(0),
		},
		Reading: &sep2.Reading{Value: i64(300)},
	})
	// V: no flow direction concept, raw magnitude, powerOfTenMultiplier -1.
	f.postInlineReading("mup-v", fleetDeviceALFDI, sep2.MirrorMeterReading{
		MRID:           "v1",
		LastUpdateTime: 100,
		ReadingType:    &sep2.ReadingType{Uom: u8(sep2.UomVolts), PowerOfTenMultiplier: i8(-1)},
		Reading:        &sep2.Reading{Value: i64(2401)}, // 240.1 V
	})
	// f: raw magnitude, powerOfTenMultiplier -2.
	f.postInlineReading("mup-f", fleetDeviceALFDI, sep2.MirrorMeterReading{
		MRID:           "f1",
		LastUpdateTime: 100,
		ReadingType:    &sep2.ReadingType{Uom: u8(33), PowerOfTenMultiplier: i8(-2)}, // 33 = Hz
		Reading:        &sep2.Reading{Value: i64(6000)},                              // 60.00 Hz
	})

	fleets := fetchFleets(t, f.handler())
	dev := findDevice(t, fleets[0], fleetDeviceALFDI)

	if dev.Measurements.P == nil || dev.Measurements.P.Value != -500 {
		t.Errorf("Measurements.P = %+v, want value -500 (forward/import negated)", dev.Measurements.P)
	}
	if dev.Measurements.P == nil || dev.Measurements.P.QualityFlags == nil || *dev.Measurements.P.QualityFlags != 0x01 {
		t.Errorf("Measurements.P.QualityFlags = %v, want 0x01", dev.Measurements.P)
	}
	if dev.Measurements.Q == nil || dev.Measurements.Q.Value != 300 {
		t.Errorf("Measurements.Q = %+v, want value 300 (reverse/export unchanged)", dev.Measurements.Q)
	}
	// float64(2401) * math.Pow10(-1) is not exactly 240.1 in binary
	// floating point; an epsilon comparison is the correct check here, not
	// a change to how the value is scaled.
	if dev.Measurements.V == nil || math.Abs(dev.Measurements.V.Value-240.1) > 1e-9 {
		t.Errorf("Measurements.V = %+v, want value ~240.1", dev.Measurements.V)
	}
	if dev.Measurements.F == nil || dev.Measurements.F.Value != 60 {
		t.Errorf("Measurements.F = %+v, want value 60", dev.Measurements.F)
	}
}

func hexBin16(v uint16) *sep2.HexBinary16 { h := sep2.HexBinary16(v); return &h }

// --- AC2: roll-up counts connected, alarmed, stale; sums only reported -------
// --- values, with the unreported count beside every sum ----------------------

func TestHandleListFleets_Rollup(t *testing.T) {
	t.Parallel()
	f := newFleetFixture(t)
	f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)
	f.assign(fleetAggregatorLFDI, fleetDeviceBLFDI)

	// Device A: connected, alarmed, and reports P (200 W export-positive).
	f.seedDevice("3", fleetDeviceALFDI, "1")
	f.setStatus("3", "1", sep2.DERStatus{
		GenConnectStatus: &sep2.ConnectStatusType{Value: 1},
		AlarmStatus:      hexBin32(0x01),
		ReadingTime:      handlerTestNow(),
	})
	f.postInlineReading("mup-a", fleetDeviceALFDI, sep2.MirrorMeterReading{
		MRID: "a-p", LastUpdateTime: 1,
		ReadingType: &sep2.ReadingType{Uom: u8(sep2.UomWatts), FlowDirection: u8(sep2.FlowDirectionReverse), PowerOfTenMultiplier: i8(0)},
		Reading:     &sep2.Reading{Value: i64(200)},
	})

	// Device B and the aggregator itself: no EndDevice/DER (unreported
	// status), no measurements either, so both counts and sums show them
	// as absent rather than zero. The fleet is the aggregator's own
	// EndDevice PLUS its managed devices, so it has three members here:
	// the aggregator, device A and device B.

	fleets := fetchFleets(t, f.handler())
	roll := fleets[0].Rollup

	if roll.DeviceCount != 3 {
		t.Errorf("DeviceCount = %d, want 3 (aggregator + 2 managed devices)", roll.DeviceCount)
	}
	if roll.Connected != 1 {
		t.Errorf("Connected = %d, want 1", roll.Connected)
	}
	if roll.Alarmed != 1 {
		t.Errorf("Alarmed = %d, want 1", roll.Alarmed)
	}
	if roll.Stale != 0 {
		t.Errorf("Stale = %d, want 0", roll.Stale)
	}
	if roll.P.Sum != 200 {
		t.Errorf("P.Sum = %v, want 200", roll.P.Sum)
	}
	if roll.P.Unreported != 2 {
		t.Errorf("P.Unreported = %d, want 2 (the aggregator and device B never reported P)", roll.P.Unreported)
	}
	if roll.StatWAvail.Unreported != 3 {
		t.Errorf("StatWAvail.Unreported = %d, want 3 (none of the three reported availability)", roll.StatWAvail.Unreported)
	}
	if roll.StatWAvail.Sum != 0 {
		t.Errorf("StatWAvail.Sum = %v, want 0 (no synthesized value for an unreported device)", roll.StatWAvail.Sum)
	}
}

// handlerTestNow gives a status a ReadingTime close enough to "now" that the
// staleness check in the roll-up does not fire; the exact value does not
// matter beyond that, since this test does not exercise staleness.
func handlerTestNow() int64 {
	return time.Now().Unix()
}

// --- AC4: a device outside the aggregator's fleet never appears in its -------
// --- roll-up, tested both ways ------------------------------------------------

func TestHandleListFleets_FleetIsolation(t *testing.T) {
	t.Parallel()
	f := newFleetFixture(t)
	f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)
	f.assign(fleetOtherAggLFDI, fleetOutsideLFDI)

	fleets := fetchFleets(t, f.handler())
	if len(fleets) != 2 {
		t.Fatalf("len(fleets) = %d, want 2", len(fleets))
	}

	var mine, other handler.Fleet
	for _, fl := range fleets {
		switch fl.AggregatorLFDI {
		case fleetAggregatorLFDI:
			mine = fl
		case fleetOtherAggLFDI:
			other = fl
		}
	}

	for _, d := range mine.Devices {
		if d.LFDI == fleetOutsideLFDI {
			t.Errorf("aggregator %q's fleet contains %q, which is outside it", fleetAggregatorLFDI, fleetOutsideLFDI)
		}
	}
	for _, d := range other.Devices {
		if d.LFDI == fleetDeviceALFDI {
			t.Errorf("aggregator %q's fleet contains %q, which belongs to a different fleet", fleetOtherAggLFDI, fleetDeviceALFDI)
		}
	}
	if mine.Rollup.DeviceCount != 2 || other.Rollup.DeviceCount != 2 {
		t.Errorf("DeviceCount = %d, %d; want 2, 2 (each fleet is its own aggregator plus its own managed device)", mine.Rollup.DeviceCount, other.Rollup.DeviceCount)
	}
}
