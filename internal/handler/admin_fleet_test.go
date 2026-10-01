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

// addDER registers an additional DER under an edevID seedDevice already
// registered, for tests that need more than one DER per device (newest-wins
// selection across DERs).
func (f *fleetFixture) addDER(edevID, derID string) {
	f.t.Helper()
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
		MRID: "a-p", LastUpdateTime: handlerTestNow(),
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

// --- #715 fix round 1 item 3: a follow-up MirrorMeterReading POST reusing an
// --- established mRID may omit ReadingType, and the reading must still be
// --- used, under the type its series was created with (2023 rule (n) /
// --- rule (h)(3)). ------------------------------------------------------------

func TestHandleListFleets_FollowUpReadingInheritsReadingTypeByMRID(t *testing.T) {
	t.Parallel()
	f := newFleetFixture(t)
	f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)

	// The creating POST: carries ReadingType, establishes mRID "p-series".
	f.postOutOfBandReading("mup-1", "r1", fleetDeviceALFDI, sep2.MirrorMeterReading{
		MRID:           "p-series",
		LastUpdateTime: 100,
		ReadingType: &sep2.ReadingType{
			Uom: u8(sep2.UomWatts), FlowDirection: u8(sep2.FlowDirectionReverse), PowerOfTenMultiplier: i8(0),
		},
		Reading: &sep2.Reading{Value: i64(400)},
	})
	// A follow-up POST reusing the same mRID, omitting ReadingType
	// entirely, with a later LastUpdateTime so it is the one that should
	// win.
	f.postOutOfBandReading("mup-1", "r2", fleetDeviceALFDI, sep2.MirrorMeterReading{
		MRID:           "p-series",
		LastUpdateTime: 200,
		Reading:        &sep2.Reading{Value: i64(450)},
	})

	fleets := fetchFleets(t, f.handler())
	dev := findDevice(t, fleets[0], fleetDeviceALFDI)

	if dev.Measurements.P == nil {
		t.Fatal("Measurements.P is nil, want the follow-up reading used under the inherited ReadingType")
	}
	if dev.Measurements.P.Value != 450 {
		t.Errorf("Measurements.P.Value = %v, want 450 (the follow-up reading, not the creating one)", dev.Measurements.P.Value)
	}
	if dev.Measurements.P.ReadingTime != 200 {
		t.Errorf("Measurements.P.ReadingTime = %d, want 200", dev.Measurements.P.ReadingTime)
	}
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

// --- #715 fix round 2 item 5: coverage gaps a pure-function test cannot
// --- reach, because they live at the call site (buildFleet's own clock
// --- read) or need real store-backed multi-record selection. ----------------

// TestHandleListFleets_UsesRealWallClockForStaleness kills a mutation
// hardcoding buildFleet's now (for example "now := int64(0)"): a device
// whose DERStatus is old by REAL wall-clock time must show up stale. Under
// a hardcoded now of 0, now-ReadingTime would be a large negative number,
// never exceeding staleAfterSeconds, so the device would wrongly read as
// fresh instead.
func TestHandleListFleets_UsesRealWallClockForStaleness(t *testing.T) {
	t.Parallel()
	f := newFleetFixture(t)
	f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)
	f.seedDevice("3", fleetDeviceALFDI, "1")
	f.setStatus("3", "1", sep2.DERStatus{
		GenConnectStatus: &sep2.ConnectStatusType{Value: 1},
		ReadingTime:      time.Now().Unix() - staleAfterSecondsForTest - 100,
	})

	fleets := fetchFleets(t, f.handler())
	if fleets[0].Rollup.Stale != 1 {
		t.Errorf("Stale = %d, want 1: a real-clock-old DERStatus must count as stale", fleets[0].Rollup.Stale)
	}
	if fleets[0].Rollup.Connected != 0 {
		t.Errorf("Connected = %d, want 0", fleets[0].Rollup.Connected)
	}
}

// staleAfterSecondsForTest mirrors admin_fleet.go's unexported
// staleAfterSeconds (15 minutes); duplicated here because handler_test is a
// separate package and the two must not silently drift, so the value is
// named, not guessed, at each use.
const staleAfterSecondsForTest = 15 * 60

// TestHandleListFleets_ConnectBitClearIsNotConnected kills "connected :=
// true" replacing the bit test: a GenConnectStatus with bit 0 clear (here
// Value 2, bit 1 set) must report Connected false, not true.
func TestHandleListFleets_ConnectBitClearIsNotConnected(t *testing.T) {
	t.Parallel()
	f := newFleetFixture(t)
	f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)
	f.seedDevice("3", fleetDeviceALFDI, "1")
	f.setStatus("3", "1", sep2.DERStatus{
		GenConnectStatus: &sep2.ConnectStatusType{Value: 2}, // bit 1 set, bit 0 (connected) clear
		ReadingTime:      handlerTestNow(),
	})

	fleets := fetchFleets(t, f.handler())
	dev := findDevice(t, fleets[0], fleetDeviceALFDI)
	if dev.Status == nil || dev.Status.Connected == nil {
		t.Fatal("Status.Connected is nil, want false")
	}
	if *dev.Status.Connected {
		t.Error("Status.Connected = true, want false: bit 0 of GenConnectStatus.Value is clear")
	}
	if fleets[0].Rollup.Connected != 0 {
		t.Errorf("Rollup.Connected = %d, want 0", fleets[0].Rollup.Connected)
	}
}

// TestHandleListFleets_NewestStatusAndAvailabilityWinAcrossDERs kills a
// flipped newest-wins comparison (">" to "<") in buildDevice: with two DERs
// under one device, the DERStatus and DERAvailability with the LATER
// readingTime must be the ones reported, not the earlier ones.
func TestHandleListFleets_NewestStatusAndAvailabilityWinAcrossDERs(t *testing.T) {
	t.Parallel()
	f := newFleetFixture(t)
	f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)
	f.seedDevice("3", fleetDeviceALFDI, "1")
	f.addDER("3", "2")

	f.setStatus("3", "1", sep2.DERStatus{
		GenConnectStatus: &sep2.ConnectStatusType{Value: 1},
		ReadingTime:      100,
	})
	f.setStatus("3", "2", sep2.DERStatus{
		GenConnectStatus: &sep2.ConnectStatusType{Value: 0}, // not connected
		ReadingTime:      200,
	})
	f.setAvailability("3", "1", sep2.DERAvailability{
		ReadingTime: 100,
		StatWAvail:  &sep2.ActivePower{Multiplier: 0, Value: 111},
	})
	f.setAvailability("3", "2", sep2.DERAvailability{
		ReadingTime: 200,
		StatWAvail:  &sep2.ActivePower{Multiplier: 0, Value: 222},
	})

	fleets := fetchFleets(t, f.handler())
	dev := findDevice(t, fleets[0], fleetDeviceALFDI)

	if dev.Status == nil || dev.Status.ReadingTime != 200 {
		t.Fatalf("Status.ReadingTime = %+v, want 200 (the newer DER's status)", dev.Status)
	}
	if dev.Status.Connected != nil && *dev.Status.Connected {
		t.Error("Status.Connected = true, want false: the newer DER's status reports not connected")
	}
	if dev.Availability == nil || dev.Availability.StatWAvail == nil || *dev.Availability.StatWAvail != 222 {
		t.Errorf("Availability = %+v, want StatWAvail 222 (the newer DER's availability)", dev.Availability)
	}
}

// TestHandleListFleets_DirectionUnknownOnTheWire reads the served JSON: every
// sum carries directionUnknown (false when known), a power sum with an
// undirected contributing reading carries true, and a V or f reading never
// carries the per-reading flag (#733).
func TestHandleListFleets_DirectionUnknownOnTheWire(t *testing.T) {
	t.Parallel()
	f := newFleetFixture(t)
	f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)
	f.assign(fleetAggregatorLFDI, fleetDeviceBLFDI)
	now := handlerTestNow()

	f.postInlineReading("mup-a", fleetDeviceALFDI, sep2.MirrorMeterReading{
		MRID: "a-p", LastUpdateTime: now,
		ReadingType: &sep2.ReadingType{Uom: u8(sep2.UomWatts), FlowDirection: u8(sep2.FlowDirectionForward), PowerOfTenMultiplier: i8(0)},
		Reading:     &sep2.Reading{Value: i64(200)},
	})
	f.postInlineReading("mup-a-v", fleetDeviceALFDI, sep2.MirrorMeterReading{
		MRID: "a-v", LastUpdateTime: now,
		ReadingType: &sep2.ReadingType{Uom: u8(sep2.UomVolts), PowerOfTenMultiplier: i8(0)},
		Reading:     &sep2.Reading{Value: i64(240)},
	})
	f.postInlineReading("mup-b", fleetDeviceBLFDI, sep2.MirrorMeterReading{
		MRID: "b-q", LastUpdateTime: now,
		ReadingType: &sep2.ReadingType{Uom: u8(sep2.UomVars), PowerOfTenMultiplier: i8(0)},
		Reading:     &sep2.Reading{Value: i64(50)},
	})

	w := httptest.NewRecorder()
	handler.HandleListFleets(f.handler())(w, httptest.NewRequest(http.MethodGet, "/api/derms/fleets", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var fleets []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &fleets); err != nil {
		t.Fatal(err)
	}
	rollup := fleets[0]["rollup"].(map[string]any)
	want := map[string]bool{"p": false, "q": true, "statWAvail": false, "statVarAvail": false}
	for key, wantFlag := range want {
		sum := rollup[key].(map[string]any)
		got, present := sum["directionUnknown"]
		if !present || got != wantFlag {
			t.Errorf("rollup.%s.directionUnknown = %v (present %v), want %v always emitted", key, got, present, wantFlag)
		}
	}
	for _, d := range fleets[0]["devices"].([]any) {
		dev := d.(map[string]any)
		meas := dev["measurements"].(map[string]any)
		if v, ok := meas["v"].(map[string]any); ok {
			if _, has := v["directionUnknown"]; has {
				t.Errorf("device %v: v reading carries directionUnknown: %v", dev["lfdi"], v)
			}
		}
	}
}
