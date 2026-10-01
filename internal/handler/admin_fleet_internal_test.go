package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Internal unit tests for admin_fleet.go's unexported helpers: the sign
// convention, staleness boundary, and roll-up accumulation are cheaper and
// more precise to pin directly than through a full HTTP round trip, and
// several of these branches (the typed-nil-adjacent roll-up paths, the
// newest-wins comparison) are exactly what mutation testing found untested
// in the #715 review (fix round 1, MEDIUM, coverage lane).

func f8(v uint8) *uint8     { return &v }
func fi8(v int8) *int8      { return &v }
func fi64(v int64) *int64   { return &v }
func fu32(v uint32) *uint32 { return &v }

func readingWithFlow(uom uint8, flow *uint8, value int64) sep2.MirrorMeterReading {
	return sep2.MirrorMeterReading{
		MRID:           "m",
		LastUpdateTime: 1,
		ReadingType:    &sep2.ReadingType{Uom: f8(uom), FlowDirection: flow, PowerOfTenMultiplier: fi8(0)},
		Reading:        &sep2.Reading{Value: fi64(value)},
	}
}

// TestConsiderMeasurement_SignConvention is #715 fix round 1 item 2: the
// EPRI client sends the signed value and derives flowDirection from its own
// sign (map_l3_get_der.c:862-870), so a wire value under Forward or Reverse
// cannot be trusted for sign and must be taken as a magnitude. The declared
// convention: export-positive = (Forward -> -1, Reverse -> +1) * abs(value);
// no flowDirection leaves the value exactly as reported.
func TestConsiderMeasurement_SignConvention(t *testing.T) {
	t.Parallel()
	forward := f8(sep2.FlowDirectionForward)
	reverse := f8(sep2.FlowDirectionReverse)

	cases := []struct {
		name string
		flow *uint8
		raw  int64
		want float64
	}{
		{"forward, positive raw", forward, 500, -500},
		{"forward, negative raw", forward, -500, -500},
		{"reverse, positive raw", reverse, 300, 300},
		{"reverse, negative raw", reverse, -300, 300},
		{"no flow direction, unchanged", nil, 150, 150},
	}

	for _, uom := range []struct {
		name string
		uom  uint8
		get  func(FleetDeviceMeasurements) *FleetMeasurement
	}{
		{"P", sep2.UomWatts, func(m FleetDeviceMeasurements) *FleetMeasurement { return m.P }},
		{"Q", sep2.UomVars, func(m FleetDeviceMeasurements) *FleetMeasurement { return m.Q }},
	} {
		for _, tc := range cases {
			t.Run(uom.name+"/"+tc.name, func(t *testing.T) {
				var out FleetDeviceMeasurements
				considerMeasurement(&out, readingWithFlow(uom.uom, tc.flow, tc.raw), Edition2018, false)
				got := uom.get(out)
				if got == nil {
					t.Fatalf("measurement is nil, want value %v", tc.want)
				}
				if got.Value != tc.want {
					t.Errorf("Value = %v, want %v", got.Value, tc.want)
				}
			})
		}
	}
}

// TestConsiderMeasurement_NewestWins pins that the later LastUpdateTime
// wins regardless of call order, not merely "the last one processed".
func TestConsiderMeasurement_NewestWins(t *testing.T) {
	t.Parallel()
	older := readingWithFlow(sep2.UomWatts, nil, 100)
	older.LastUpdateTime = 10
	newer := readingWithFlow(sep2.UomWatts, nil, 200)
	newer.LastUpdateTime = 20

	t.Run("newer processed second", func(t *testing.T) {
		var out FleetDeviceMeasurements
		considerMeasurement(&out, older, Edition2018, false)
		considerMeasurement(&out, newer, Edition2018, false)
		if out.P == nil || out.P.Value != 200 {
			t.Errorf("P = %+v, want value 200 (the newer reading)", out.P)
		}
	})

	t.Run("newer processed first", func(t *testing.T) {
		var out FleetDeviceMeasurements
		considerMeasurement(&out, newer, Edition2018, false)
		considerMeasurement(&out, older, Edition2018, false)
		if out.P == nil || out.P.Value != 200 {
			t.Errorf("P = %+v, want value 200 (the newer reading must not be overwritten by an older one processed later)", out.P)
		}
	})
}

// TestConsiderMeasurement_SkipsInvalidReadings pins that a reading missing
// any of ReadingType, Uom, Reading or Reading.Value contributes nothing,
// and an unrecognized Uom is ignored rather than misfiled.
func TestConsiderMeasurement_SkipsInvalidReadings(t *testing.T) {
	t.Parallel()
	valid := readingWithFlow(sep2.UomWatts, nil, 1)

	cases := []struct {
		name   string
		mutate func(sep2.MirrorMeterReading) sep2.MirrorMeterReading
	}{
		{"nil ReadingType", func(r sep2.MirrorMeterReading) sep2.MirrorMeterReading { r.ReadingType = nil; return r }},
		{"nil Uom", func(r sep2.MirrorMeterReading) sep2.MirrorMeterReading { r.ReadingType.Uom = nil; return r }},
		{"nil Reading", func(r sep2.MirrorMeterReading) sep2.MirrorMeterReading { r.Reading = nil; return r }},
		{"nil Reading.Value", func(r sep2.MirrorMeterReading) sep2.MirrorMeterReading { r.Reading.Value = nil; return r }},
		{"unrecognized Uom", func(r sep2.MirrorMeterReading) sep2.MirrorMeterReading { r.ReadingType.Uom = f8(99); return r }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Each case gets its own deep-enough copy so mutating one
			// field does not corrupt a sibling subtest's fixture.
			r := valid
			rt := *valid.ReadingType
			r.ReadingType = &rt
			rd := *valid.Reading
			r.Reading = &rd
			r = tc.mutate(r)

			var out FleetDeviceMeasurements
			considerMeasurement(&out, r, Edition2018, false)
			if out.P != nil || out.Q != nil || out.V != nil || out.F != nil {
				t.Errorf("measurements = %+v, want all nil for an invalid reading", out)
			}
		})
	}
}

// TestAccumulateRollup_ConnectBit pins that only bit 0 of GenConnectStatus
// counts as connected, and that a device with the bit clear does not count.
func TestAccumulateRollup_ConnectBit(t *testing.T) {
	t.Parallel()
	now := int64(1000)

	cases := []struct {
		name      string
		connected *bool
		want      int
	}{
		{"connected", boolPtr(true), 1},
		{"not connected", boolPtr(false), 0},
		{"no status at all", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rollup FleetRollup
			var status *FleetDeviceStatus
			if tc.connected != nil {
				status = &FleetDeviceStatus{Connected: tc.connected, ReadingTime: now}
			}
			accumulateRollup(&rollup, FleetDevice{Status: status}, now)
			if rollup.Connected != tc.want {
				t.Errorf("Connected = %d, want %d", rollup.Connected, tc.want)
			}
		})
	}
}

func boolPtr(v bool) *bool { return &v }

// TestAccumulateRollup_AlarmZeroVsNonzero pins that an explicit zero alarm
// value does not count as alarmed, only a nonzero one does.
func TestAccumulateRollup_AlarmZeroVsNonzero(t *testing.T) {
	t.Parallel()
	now := int64(1000)

	cases := []struct {
		name  string
		alarm *uint32
		want  int
	}{
		{"zero alarm status", fu32(0), 0},
		{"nonzero alarm status", fu32(1), 1},
		{"no alarm status reported", nil, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var rollup FleetRollup
			status := &FleetDeviceStatus{AlarmStatus: tc.alarm, ReadingTime: now}
			accumulateRollup(&rollup, FleetDevice{Status: status}, now)
			if rollup.Alarmed != tc.want {
				t.Errorf("Alarmed = %d, want %d", rollup.Alarmed, tc.want)
			}
		})
	}
}

// TestAccumulateRollup_StaleButConnected is #715 fix round 1 item 5: a
// device whose last known status is stale counts as stale, never as
// connected, even though its stored Connected bit is still set (it is the
// last thing the device reported, not a live fact).
func TestAccumulateRollup_StaleButConnected(t *testing.T) {
	t.Parallel()
	now := int64(100000)
	status := &FleetDeviceStatus{Connected: boolPtr(true), ReadingTime: now - staleAfterSeconds - 1}

	var rollup FleetRollup
	accumulateRollup(&rollup, FleetDevice{Status: status}, now)

	if rollup.Stale != 1 {
		t.Errorf("Stale = %d, want 1", rollup.Stale)
	}
	if rollup.Connected != 0 {
		t.Errorf("Connected = %d, want 0: a stale reading must not also count as connected", rollup.Connected)
	}
}

// TestStaleness_BothSidesOfBoundary pins the exact edge: the comparison is
// strictly greater-than, so a reading exactly staleAfterSeconds old is NOT
// yet stale, and one second past it is.
func TestStaleness_BothSidesOfBoundary(t *testing.T) {
	t.Parallel()
	now := int64(100000)

	t.Run("exactly at the boundary is not stale", func(t *testing.T) {
		var rollup FleetRollup
		status := &FleetDeviceStatus{Connected: boolPtr(true), ReadingTime: now - staleAfterSeconds}
		accumulateRollup(&rollup, FleetDevice{Status: status}, now)
		if rollup.Stale != 0 || rollup.Connected != 1 {
			t.Errorf("Stale = %d, Connected = %d; want 0, 1 at exactly the boundary", rollup.Stale, rollup.Connected)
		}
	})

	t.Run("one second past the boundary is stale", func(t *testing.T) {
		var rollup FleetRollup
		status := &FleetDeviceStatus{Connected: boolPtr(true), ReadingTime: now - staleAfterSeconds - 1}
		accumulateRollup(&rollup, FleetDevice{Status: status}, now)
		if rollup.Stale != 1 || rollup.Connected != 0 {
			t.Errorf("Stale = %d, Connected = %d; want 1, 0 one second past the boundary", rollup.Stale, rollup.Connected)
		}
	})
}

// TestNowUnix_IsWallClock is a control, not a behavior pin: it proves
// nowUnix is wired to the real clock (so a test that overrides it is
// actually overriding what production reads), by bracketing it between two
// time.Now() reads.
func TestNowUnix_IsWallClock(t *testing.T) {
	before := time.Now().Unix()
	got := nowUnix()
	after := time.Now().Unix()
	if got < before || got > after {
		t.Errorf("nowUnix() = %d, want between %d and %d", got, before, after)
	}
}

// TestAccumulateRollup_QSumAndStatVarAvailFromRightField pins that the Q
// measurement feeds rollup.Q (not P), and that DERAvailability's
// StatVarAvail feeds rollup.StatVarAvail specifically, not StatWAvail: a
// field-swap mutation would leave one sum right and the other silently
// wrong.
func TestAccumulateRollup_QSumAndStatVarAvailFromRightField(t *testing.T) {
	t.Parallel()
	now := int64(1000)
	statW := 111.0
	statVar := 222.0

	var rollup FleetRollup
	dev := FleetDevice{
		Measurements: FleetDeviceMeasurements{Q: &FleetMeasurement{Value: 50, ReadingTime: now}},
		Availability: &FleetDeviceAvailability{StatWAvail: &statW, StatVarAvail: &statVar, ReadingTime: now},
	}
	accumulateRollup(&rollup, dev, now)

	if rollup.Q.Sum != 50 {
		t.Errorf("Q.Sum = %v, want 50", rollup.Q.Sum)
	}
	if rollup.P.Sum != 0 || rollup.P.Unreported != 1 {
		t.Errorf("P = %+v, want untouched by the Q reading (Sum 0, Unreported 1)", rollup.P)
	}
	if rollup.StatWAvail.Sum != 111 {
		t.Errorf("StatWAvail.Sum = %v, want 111", rollup.StatWAvail.Sum)
	}
	if rollup.StatVarAvail.Sum != 222 {
		t.Errorf("StatVarAvail.Sum = %v, want 222 (from StatVarAvail, not StatWAvail)", rollup.StatVarAvail.Sum)
	}
}

// TestAccumulateRollup_AvailabilityStalenessIsItsOwnClock is #715 fix round
// 4 item 1: StatWAvail/StatVarAvail must be judged from DERAvailability's
// own ReadingTime, not the device's DERStatus.ReadingTime. DERStatus and
// DERAvailability are two independently client-set PUT resources (neither
// carries a server-stamped receipt time; see accumulateRollup's doc
// comment) and can drift apart from each other arbitrarily.
func TestAccumulateRollup_AvailabilityStalenessIsItsOwnClock(t *testing.T) {
	t.Parallel()
	now := int64(1000000)
	fresh := now
	twoDaysOld := now - 2*24*60*60
	staleTime := now - staleAfterSeconds - 1

	t.Run("fresh status, 2-day-old availability: stale", func(t *testing.T) {
		statW := 700.0
		dev := FleetDevice{
			Status:       &FleetDeviceStatus{ReadingTime: fresh},
			Availability: &FleetDeviceAvailability{StatWAvail: &statW, ReadingTime: twoDaysOld},
		}
		var rollup FleetRollup
		accumulateRollup(&rollup, dev, now)
		if rollup.StatWAvail.Stale != 1 || rollup.StatWAvail.Sum != 0 {
			t.Errorf("StatWAvail = %+v, want Stale 1, Sum 0", rollup.StatWAvail)
		}
	})

	t.Run("stale status, fresh availability: not stale", func(t *testing.T) {
		statW := 700.0
		dev := FleetDevice{
			Status:       &FleetDeviceStatus{ReadingTime: staleTime},
			Availability: &FleetDeviceAvailability{StatWAvail: &statW, ReadingTime: fresh},
		}
		var rollup FleetRollup
		accumulateRollup(&rollup, dev, now)
		if rollup.StatWAvail.Stale != 0 || rollup.StatWAvail.Sum != 700 {
			t.Errorf("StatWAvail = %+v, want Stale 0, Sum 700", rollup.StatWAvail)
		}
	})

	t.Run("no status at all: uses availability's own time", func(t *testing.T) {
		statW := 700.0
		dev := FleetDevice{
			Status:       nil,
			Availability: &FleetDeviceAvailability{StatWAvail: &statW, ReadingTime: fresh},
		}
		var rollup FleetRollup
		accumulateRollup(&rollup, dev, now)
		if rollup.StatWAvail.Stale != 0 || rollup.StatWAvail.Sum != 700 {
			t.Errorf("StatWAvail = %+v, want Stale 0, Sum 700 (no DERStatus to judge, but availability is fresh)", rollup.StatWAvail)
		}
	})
}

// TestAccumulateRollup_MeasurementStalenessIsPerValueNotDeviceStatus is
// #715 fix round 2 item 4's fix, both directions:
//   - a stale DERStatus must not drop an otherwise-fresh P/Q reading (the
//     "901 s behind drops fresh power" bug), because P and Q are judged on
//     their own server-stamped reading time; and
//   - a device with NO DERStatus at all is not automatically "never stale"
//     for P/Q the way it is for the status/alarm/connected bucket: an old
//     mirror reading is still stale by its own clock.
func TestAccumulateRollup_MeasurementStalenessIsPerValueNotDeviceStatus(t *testing.T) {
	t.Parallel()
	now := int64(100000)

	t.Run("fresh P survives a stale DERStatus", func(t *testing.T) {
		dev := FleetDevice{
			Status:       &FleetDeviceStatus{ReadingTime: now - staleAfterSeconds - 1}, // stale
			Measurements: FleetDeviceMeasurements{P: &FleetMeasurement{Value: 700, ReadingTime: now}},
		}
		var rollup FleetRollup
		accumulateRollup(&rollup, dev, now)

		if rollup.Stale != 1 {
			t.Errorf("Stale = %d, want 1: the device's own status is still stale", rollup.Stale)
		}
		if rollup.P.Sum != 700 || rollup.P.Stale != 0 {
			t.Errorf("P = %+v, want Sum 700, Stale 0: a fresh reading must not be dropped by a stale DERStatus", rollup.P)
		}
	})

	t.Run("stale P with no DERStatus at all", func(t *testing.T) {
		dev := FleetDevice{
			Status:       nil,
			Measurements: FleetDeviceMeasurements{P: &FleetMeasurement{Value: 700, ReadingTime: now - staleAfterSeconds - 1}},
		}
		var rollup FleetRollup
		accumulateRollup(&rollup, dev, now)

		if rollup.Stale != 0 {
			t.Errorf("Stale = %d, want 0: the device-level bucket has no status to judge", rollup.Stale)
		}
		if rollup.P.Sum != 0 || rollup.P.Stale != 1 {
			t.Errorf("P = %+v, want Sum 0, Stale 1: an old mirror reading is stale on its own clock even with no DERStatus at all", rollup.P)
		}
	})
}

// --- #715 fix round 1 item 6: store read errors must be logged, and a
// --- genuine failure must never read the same as "never reported"
// --- (pkg/store's absent-versus-failed contract). -----------------------------

// erroringScopedReader implements store.ScopedReader[T], always failing with
// err. It never returns store.ErrNotFound unless err is that sentinel, so it
// drives exactly the "genuine failure" branch admin_fleet.go must not treat
// the same as an ordinary absence.
type erroringScopedReader[T store.Copier[T]] struct{ err error }

func (e erroringScopedReader[T]) Get(context.Context, string, string) (T, error) {
	var zero T
	return zero, e.err
}

func (e erroringScopedReader[T]) List(context.Context, string, store.ListOptions) (store.ListResult[T], error) {
	return store.ListResult[T]{}, e.err
}

func (e erroringScopedReader[T]) Count(context.Context, string) (uint32, error) { return 0, e.err }

func (e erroringScopedReader[T]) HasParent(context.Context, string) (bool, error) {
	return false, e.err
}

func (e erroringScopedReader[T]) Parents(context.Context) ([]string, error) { return nil, e.err }

// erroringResourceReader implements store.ResourceReader[T], always failing
// with err.
type erroringResourceReader[T store.Copier[T]] struct{ err error }

func (e erroringResourceReader[T]) Get(context.Context, string) (T, error) {
	var zero T
	return zero, e.err
}

func (e erroringResourceReader[T]) List(context.Context, store.ListOptions) (store.ListResult[T], error) {
	return store.ListResult[T]{}, e.err
}

func (e erroringResourceReader[T]) Count(context.Context) (uint32, error) { return 0, e.err }

// erroringEndDevices implements store.EndDeviceReader, always failing with
// err.
type erroringEndDevices struct{ err error }

func (e erroringEndDevices) Get(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, e.err
}

func (e erroringEndDevices) List(context.Context, store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	return store.ListResult[sep2.EndDevice]{}, e.err
}

func (e erroringEndDevices) Count(context.Context) (uint32, error) { return 0, e.err }

func (e erroringEndDevices) GetBySFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, e.err
}

func (e erroringEndDevices) GetByLFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{}, e.err
}

// erroringManagers implements fleetManagerReader, always failing ManagedBy
// with err. Managers() returns one aggregator, so HandleListFleets reaches
// the failing buildFleet call: the concrete memory store's own Managers and
// ManagedBy never fail, so this is the only way to drive the branch.
type erroringManagers struct{ err error }

func (e erroringManagers) ManagedBy(context.Context, string) ([]string, error) { return nil, e.err }
func (e erroringManagers) Managers(context.Context) []string {
	return []string{"AAAA000000000000000000000000000000000001"}
}

func captureLog(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

func TestHandleListFleets_ManagedByErrorReturns500AndLogs(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{Managers: erroringManagers{err: errors.New("backend unreachable")}}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/derms/fleets", nil)
	HandleListFleets(h)(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body = %s", w.Code, w.Body.String())
	}
	if strings.TrimSpace(w.Body.String()) == "[]" {
		t.Error("body is an empty fleet list: a ManagedBy failure must not read as an empty fleet")
	}
	if !strings.Contains(buf.String(), "backend unreachable") {
		t.Errorf("log output = %q, want it to name the ManagedBy failure", buf.String())
	}
}

func TestLatestStatus_GenuineFailureIsReturnedAndLogged(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{DERStatuses: erroringScopedReader[sep2.DERStatus]{err: errors.New("disk read failed")}}

	got, err := h.latestStatus(context.Background(), "3/1")
	if got != nil || err == nil || !strings.Contains(err.Error(), "disk read failed") {
		t.Errorf("latestStatus = %+v, %v; want nil and an error naming the failure", got, err)
	}
	if !strings.Contains(buf.String(), "disk read failed") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

func TestLatestStatus_ErrNotFoundIsTheOrdinaryCaseAndSilent(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{DERStatuses: erroringScopedReader[sep2.DERStatus]{err: store.ErrNotFound}}

	got, err := h.latestStatus(context.Background(), "3/1")
	if got != nil || err != nil {
		t.Errorf("latestStatus = %+v, %v, want nil, nil", got, err)
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none: a device with no DERStatus yet is the ordinary case, not a failure", buf.String())
	}
}

func TestLatestAvailability_GenuineFailureIsReturnedAndLogged(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{DERAvailabilities: erroringScopedReader[sep2.DERAvailability]{err: errors.New("disk read failed")}}

	got, err := h.latestAvailability(context.Background(), "3/1")
	if got != nil || err == nil || !strings.Contains(err.Error(), "disk read failed") {
		t.Errorf("latestAvailability = %+v, %v; want nil and an error naming the failure", got, err)
	}
	if !strings.Contains(buf.String(), "disk read failed") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

func TestLatestAvailability_ErrNotFoundIsTheOrdinaryCaseAndSilent(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{DERAvailabilities: erroringScopedReader[sep2.DERAvailability]{err: store.ErrNotFound}}

	got, err := h.latestAvailability(context.Background(), "3/1")
	if got != nil || err != nil {
		t.Errorf("latestAvailability = %+v, %v, want nil, nil", got, err)
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none", buf.String())
	}
}

func TestBuildDevice_EndDevicesGenuineFailureIsReturnedAndLogged(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{EndDevices: erroringEndDevices{err: errors.New("lookup backend down")}}

	_, err := h.buildDevice(context.Background(), fleetTestLFDI)
	if err == nil || !strings.Contains(err.Error(), "lookup backend down") {
		t.Errorf("buildDevice error = %v, want one naming the failure", err)
	}
	if !strings.Contains(buf.String(), "lookup backend down") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

func TestBuildDevice_EndDevicesErrNotFoundIsTheOrdinaryCaseAndSilent(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{EndDevices: erroringEndDevices{err: store.ErrNotFound}}

	fd, err := h.buildDevice(context.Background(), fleetTestLFDI)
	if err != nil || fd.LFDI != fleetTestLFDI {
		t.Errorf("buildDevice = %+v, %v, want the bare device and no error", fd, err)
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none: a managed LFDI with no EndDevice record is the ordinary case", buf.String())
	}
}

func TestBuildDevice_DERsListFailureIsReturnedAndLogged(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{
		EndDevices: erroringEndDevices{}, // nil err: GetByLFDI succeeds with a zero-value EndDevice
		DERs:       erroringScopedReader[sep2.DER]{err: errors.New("der list backend down")},
	}

	_, err := h.buildDevice(context.Background(), fleetTestLFDI)
	if err == nil || !strings.Contains(err.Error(), "der list backend down") {
		t.Errorf("buildDevice error = %v, want one naming the failure: DERs.List never fails for an unknown parent, so any error here is genuine", err)
	}
	if !strings.Contains(buf.String(), "der list backend down") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

func TestDeviceMeasurements_MirrorUsagePointsListFailureIsReturnedAndLogged(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{MirrorUsagePoints: erroringResourceReader[sep2.MirrorUsagePoint]{err: errors.New("mup list backend down")}}

	got, err := h.deviceMeasurements(context.Background(), fleetTestLFDI)
	if err == nil || !strings.Contains(err.Error(), "mup list backend down") {
		t.Errorf("deviceMeasurements error = %v, want one naming the failure", err)
	}
	if got.P != nil || got.Q != nil || got.V != nil || got.F != nil {
		t.Errorf("deviceMeasurements = %+v, want all nil", got)
	}
	if !strings.Contains(buf.String(), "mup list backend down") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

func TestDeviceMeasurements_MirrorMeterReadingsListFailureIsReturnedAndLogged(t *testing.T) {
	buf := captureLog(t)
	mups := memoryMirrorUsagePointsFor(t, fleetTestLFDI)
	h := &AdminFleetHandler{
		MirrorUsagePoints:   mups,
		MirrorMeterReadings: erroringScopedReader[sep2.MirrorMeterReading]{err: errors.New("mmr list backend down")},
	}

	_, err := h.deviceMeasurements(context.Background(), fleetTestLFDI)
	if err == nil || !strings.Contains(err.Error(), "mmr list backend down") {
		t.Errorf("deviceMeasurements error = %v, want one naming the failure", err)
	}
	if !strings.Contains(buf.String(), "mmr list backend down") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

// fleetOf500 drives HandleListFleets over h with one aggregator and returns
// the recorded response.
func fleetOf500(h *AdminFleetHandler) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	HandleListFleets(h)(w, httptest.NewRequest(http.MethodGet, "/api/derms/fleets", nil))
	return w
}

// registeredEndDevices serves one EndDevice at /edev/3 for any LFDI, so
// buildDevice proceeds to the DER reads.
type registeredEndDevices struct{ erroringEndDevices }

func (registeredEndDevices) GetByLFDI(context.Context, string) (sep2.EndDevice, error) {
	return sep2.EndDevice{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/3"}}}, nil
}

// oneDER lists a single DER under any parent so the status and availability
// reads are reached.
type oneDER struct{ erroringScopedReader[sep2.DER] }

func (oneDER) List(context.Context, string, store.ListOptions) (store.ListResult[sep2.DER], error) {
	return store.ListResult[sep2.DER]{Items: []sep2.DER{{SubscribableResource: sep2.SubscribableResource{Resource: sep2.Resource{Href: "/edev/3/der/1"}}}}}, nil
}

// TestHandleListFleets_StoreReadFailureIs500 is #731: each of the six
// per-device store reads, failing with a non-NotFound error, fails the whole
// request with 500 and never a 200 listing the device without its data; the
// same reads failing with ErrNotFound stay a 200.
func TestHandleListFleets_StoreReadFailureIs500(t *testing.T) {
	boom := errors.New("backend down")
	cases := []struct {
		name string
		// getRead marks the single-item Get reads, where ErrNotFound is the
		// ordinary "no data yet" case; a List read never returns it (the
		// store's List contract), so any List error is a failure.
		getRead bool
		build   func(err error) *AdminFleetHandler
	}{
		{"EndDevices.GetByLFDI", true, func(err error) *AdminFleetHandler {
			return &AdminFleetHandler{EndDevices: erroringEndDevices{err: err}}
		}},
		{"DERs.List", false, func(err error) *AdminFleetHandler {
			return &AdminFleetHandler{EndDevices: registeredEndDevices{}, DERs: erroringScopedReader[sep2.DER]{err: err}}
		}},
		{"DERStatuses.Get", true, func(err error) *AdminFleetHandler {
			return &AdminFleetHandler{EndDevices: registeredEndDevices{}, DERs: oneDER{}, DERStatuses: erroringScopedReader[sep2.DERStatus]{err: err}}
		}},
		{"DERAvailabilities.Get", true, func(err error) *AdminFleetHandler {
			return &AdminFleetHandler{EndDevices: registeredEndDevices{}, DERs: oneDER{}, DERAvailabilities: erroringScopedReader[sep2.DERAvailability]{err: err}}
		}},
		{"MirrorUsagePoints.List", false, func(err error) *AdminFleetHandler {
			return &AdminFleetHandler{MirrorUsagePoints: erroringResourceReader[sep2.MirrorUsagePoint]{err: err}}
		}},
		{"MirrorMeterReadings.List", false, func(err error) *AdminFleetHandler {
			return &AdminFleetHandler{
				MirrorUsagePoints:   memoryMirrorUsagePointsFor(t, fleetTestLFDI),
				MirrorMeterReadings: erroringScopedReader[sep2.MirrorMeterReading]{err: err},
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			captureLog(t)
			h := tc.build(boom)
			h.Managers = fleetOneAggregator{lfdi: fleetTestLFDI}
			w := fleetOf500(h)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500; body = %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), fleetTestLFDI) {
				t.Errorf("body = %s, want no fleet listing", w.Body.String())
			}

			if !tc.getRead {
				return
			}
			h = tc.build(store.ErrNotFound)
			h.Managers = fleetOneAggregator{lfdi: fleetTestLFDI}
			w = fleetOf500(h)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), fleetTestLFDI) {
				t.Errorf("ErrNotFound: status = %d, body = %s, want 200 listing the device", w.Code, w.Body.String())
			}
		})
	}
}

// fleetOneAggregator is a fleetManagerReader with one aggregator managing
// nobody, so the fleet holds exactly the aggregator's own device.
type fleetOneAggregator struct{ lfdi string }

func (f fleetOneAggregator) ManagedBy(context.Context, string) ([]string, error) { return nil, nil }
func (f fleetOneAggregator) Managers(context.Context) []string                   { return []string{f.lfdi} }

const fleetTestLFDI = "D001000000000000000000000000000000000001"

// memoryMirrorUsagePointsFor returns a store.ResourceReader[sep2.MirrorUsagePoint]
// holding one MirrorUsagePoint for lfdi, so a test can reach the
// MirrorMeterReadings.List call downstream of it.
func memoryMirrorUsagePointsFor(t *testing.T, lfdi string) store.ResourceReader[sep2.MirrorUsagePoint] {
	t.Helper()
	s := memory.NewStore[sep2.MirrorUsagePoint]()
	mup := sep2.MirrorUsagePoint{Resource: sep2.Resource{Href: "/mup/1"}, DeviceLFDI: lfdi}
	if err := s.Create(context.Background(), "1", mup); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s
}

// --- #715 fix round 2 items 1 and 2: inheritReadingTypeByMRID must not
// --- depend on which reading in the slice happens to carry ReadingType
// --- first: a rule (a)(4) re-POST rewrites the inline readings, so a newer
// --- untyped inline reading can precede an older typed out-of-band one in
// --- whatever order deviceMeasurements happens to assemble them in. -----------

func typedReading(mrid string, updateTime int64, uom uint8, flow *uint8, value int64) sep2.MirrorMeterReading {
	return sep2.MirrorMeterReading{
		MRID:           mrid,
		LastUpdateTime: updateTime,
		ReadingType:    &sep2.ReadingType{Uom: f8(uom), FlowDirection: flow, PowerOfTenMultiplier: fi8(0)},
		Reading:        &sep2.Reading{Value: fi64(value)},
	}
}

func untypedReading(mrid string, updateTime int64, value int64) sep2.MirrorMeterReading {
	return sep2.MirrorMeterReading{
		MRID:           mrid,
		LastUpdateTime: updateTime,
		Reading:        &sep2.Reading{Value: fi64(value)},
	}
}

// TestInheritReadingTypeByMRID_OrderIndependent covers both orderings: the
// typed reading appearing before the untyped one in the slice (what
// deviceMeasurements happens to produce when nothing has been re-POSTed),
// and the untyped one appearing first (what a rule (a)(4) re-POST of the
// inline reading can produce, since it can carry a LATER LastUpdateTime than
// an existing out-of-band reading while still landing earlier in the slice
// deviceMeasurements builds, inline-then-out-of-band).
func TestInheritReadingTypeByMRID_OrderIndependent(t *testing.T) {
	t.Parallel()
	typed := typedReading("series-1", 100, sep2.UomWatts, nil, 400)
	untyped := untypedReading("series-1", 200, 450)

	t.Run("typed first", func(t *testing.T) {
		readings := []sep2.MirrorMeterReading{typed, untyped}
		inheritReadingTypeByMRID(readings)
		if readings[1].ReadingType == nil {
			t.Fatal("the untyped reading's ReadingType is still nil")
		}
		if *readings[1].ReadingType.Uom != sep2.UomWatts {
			t.Errorf("inherited Uom = %v, want Watts", *readings[1].ReadingType.Uom)
		}
	})

	t.Run("untyped first", func(t *testing.T) {
		readings := []sep2.MirrorMeterReading{untyped, typed}
		inheritReadingTypeByMRID(readings)
		if readings[0].ReadingType == nil {
			t.Fatal("the untyped reading's ReadingType is still nil: inheritance must not depend on slice order")
		}
		if *readings[0].ReadingType.Uom != sep2.UomWatts {
			t.Errorf("inherited Uom = %v, want Watts", *readings[0].ReadingType.Uom)
		}
	})
}

// TestInheritReadingTypeByMRID_DoesNotCrossContaminateDifferentMRIDs kills a
// mutation collapsing the per-reading mRID key to a constant (for example
// ""): two distinct series must never inherit each other's type.
func TestInheritReadingTypeByMRID_DoesNotCrossContaminateDifferentMRIDs(t *testing.T) {
	t.Parallel()
	seriesA := typedReading("series-A", 100, sep2.UomWatts, nil, 400)
	seriesB := untypedReading("series-B", 100, 999) // never typed; a different, unrelated series

	readings := []sep2.MirrorMeterReading{seriesA, seriesB}
	inheritReadingTypeByMRID(readings)

	if readings[1].ReadingType != nil {
		t.Errorf("series-B inherited a ReadingType (%+v) from series-A; the two series share no mRID", readings[1].ReadingType)
	}

	var out FleetDeviceMeasurements
	considerMeasurement(&out, readings[1], Edition2018, false)
	if out.P != nil {
		t.Errorf("Measurements.P = %+v, want nil: an untyped reading with no established series contributes nothing", out.P)
	}
}

// TestInheritReadingTypeByMRID_ReverseUntypedFollowUp is #715 fix round 2
// item 1's Reverse case: once inheritance and the export-positive mapping
// both apply, an untyped follow-up reading of -300 under a Reverse-typed
// series reports +300 (Reverse = the fleet exporting = already
// export-positive; abs() discards the sign the untyped reading happened to
// carry, per the fix round 1 sign convention).
func TestInheritReadingTypeByMRID_ReverseUntypedFollowUp(t *testing.T) {
	t.Parallel()
	reverse := f8(sep2.FlowDirectionReverse)
	readings := []sep2.MirrorMeterReading{
		typedReading("series-r", 100, sep2.UomWatts, reverse, 50),
		untypedReading("series-r", 200, -300),
	}
	inheritReadingTypeByMRID(readings)

	var out FleetDeviceMeasurements
	for i := range readings {
		considerMeasurement(&out, readings[i], Edition2018, false)
	}
	if out.P == nil || out.P.Value != 300 {
		t.Errorf("Measurements.P = %+v, want value 300", out.P)
	}
}

// TestHandleListFleets_InlineCreateThenOutOfBandFollowUpInheritsType is
// #715 fix round 2 item 2: the realistic shape is an inline reading from
// the creating POST /mup, followed by an out-of-band POST /mup/{id}/mr
// reusing that mRID without ReadingType.
func TestHandleListFleets_InlineCreateThenOutOfBandFollowUpInheritsType(t *testing.T) {
	t.Parallel()
	mups := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrs := memory.NewScopedStore[sep2.MirrorMeterReading]()
	h := &AdminFleetHandler{MirrorUsagePoints: mups, MirrorMeterReadings: mmrs}

	mup := sep2.MirrorUsagePoint{
		Resource:   sep2.Resource{Href: "/mup/1"},
		DeviceLFDI: fleetTestLFDI,
		MirrorMeterReading: []sep2.MirrorMeterReading{
			typedReading("inline-series", 100, sep2.UomWatts, f8(sep2.FlowDirectionReverse), 400),
		},
	}
	if err := mups.Create(context.Background(), "1", mup); err != nil {
		t.Fatalf("MirrorUsagePoints.Create: %v", err)
	}
	if err := mmrs.Create(context.Background(), "1", "r2", untypedReading("inline-series", 200, 450)); err != nil {
		t.Fatalf("MirrorMeterReadings.Create: %v", err)
	}

	got, err := h.deviceMeasurements(context.Background(), fleetTestLFDI)
	if err != nil {
		t.Fatalf("deviceMeasurements: %v", err)
	}
	if got.P == nil || got.P.Value != 450 {
		t.Errorf("Measurements.P = %+v, want value 450 (the out-of-band follow-up, inheriting the inline creating reading's type)", got.P)
	}
}

// --- #715 fix round 2 item 3: pin Stale for all four sums, and pin that
// --- addToSum checks staleness before "value == nil" (a swap would report a
// --- stale-but-unreported value as Unreported instead of Stale). -------------

// TestAccumulateRollup_AllFourSumsStale kills a mutation that only excludes
// one sum from staleness (for example passing false for Q, StatWAvail or
// StatVarAvail) by giving all four a value and checking all four land in
// Stale, not Sum or Unreported.
func TestAccumulateRollup_AllFourSumsStale(t *testing.T) {
	t.Parallel()
	now := int64(100000)
	oldTime := now - staleAfterSeconds - 1
	statW, statVar := 111.0, 222.0
	dev := FleetDevice{
		Status: &FleetDeviceStatus{ReadingTime: oldTime},
		Measurements: FleetDeviceMeasurements{
			P: &FleetMeasurement{Value: 700, ReadingTime: oldTime},
			Q: &FleetMeasurement{Value: 50, ReadingTime: oldTime},
		},
		Availability: &FleetDeviceAvailability{StatWAvail: &statW, StatVarAvail: &statVar, ReadingTime: oldTime},
	}

	var rollup FleetRollup
	accumulateRollup(&rollup, dev, now)

	for name, sum := range map[string]FleetSum{
		"P": rollup.P, "Q": rollup.Q, "StatWAvail": rollup.StatWAvail, "StatVarAvail": rollup.StatVarAvail,
	} {
		if sum.Sum != 0 || sum.Stale != 1 || sum.Unreported != 0 {
			t.Errorf("%s = %+v, want Sum 0, Stale 1, Unreported 0", name, sum)
		}
	}
}

// TestAddToSum_StaleTakesPrecedenceOverUnreported kills a swap of addToSum's
// case order: a stale device with no value for this quantity must count as
// Stale, never Unreported, because "we know the last value is too old to
// trust" and "we have never heard a value at all" are different facts.
func TestAddToSum_StaleTakesPrecedenceOverUnreported(t *testing.T) {
	t.Parallel()
	var sum FleetSum
	addToSum(&sum, nil, true, false)
	if sum.Stale != 1 {
		t.Errorf("Stale = %d, want 1", sum.Stale)
	}
	if sum.Unreported != 0 {
		t.Errorf("Unreported = %d, want 0: stale must be checked before the unreported case", sum.Unreported)
	}
}

// TestConsiderMeasurement_UnrecognizedFlowDirectionLeavesValueUnchanged
// kills a default case added to the flowDirection switch that would flip
// the sign for any code other than Forward or Reverse: only those two codes
// are defined by the export-positive mapping (#715 fix round 1 item 2), so
// anything else must pass through as scaledValue computed it, the same as
// no flowDirection at all.
func TestConsiderMeasurement_UnrecognizedFlowDirectionLeavesValueUnchanged(t *testing.T) {
	t.Parallel()
	const unrecognizedFlowDirection uint8 = 12 // neither FlowDirectionForward (1) nor FlowDirectionReverse (19)
	// A positive raw value, deliberately: Forward's mapping is also
	// "-magnitude", so a negative raw value would leave a default case that
	// copies Forward's behavior indistinguishable from the correct
	// unchanged result. Only a positive input, where "unchanged" (+75) and
	// "-magnitude" (-75) disagree, proves no sign flip happened.
	reading := typedReading("series", 100, sep2.UomWatts, f8(unrecognizedFlowDirection), 75)

	var out FleetDeviceMeasurements
	considerMeasurement(&out, reading, Edition2018, false)

	if out.P == nil || out.P.Value != 75 {
		t.Errorf("Measurements.P = %+v, want value 75 (unchanged; no defined mapping for flowDirection %d)", out.P, unrecognizedFlowDirection)
	}
}

// --- #715 fix round 3 item 2: the declared-edition flowDirection mapping,
// --- operator decision on #715. ------------------------------------------

// TestConsiderMeasurement_EditionFlowDirectionMapping covers every
// combination the decision names: a forward and a reverse reading under
// each edition, and a non-DER mirror under 2023 (which must keep the 2018
// mapping, not the DER-flipped one).
func TestConsiderMeasurement_EditionFlowDirectionMapping(t *testing.T) {
	t.Parallel()
	forward := f8(sep2.FlowDirectionForward)
	reverse := f8(sep2.FlowDirectionReverse)

	cases := []struct {
		name    string
		edition SEP2Edition
		isDER   bool
		flow    *uint8
		want    float64
	}{
		{"2018 forward is import (negative)", Edition2018, false, forward, -100},
		{"2018 reverse is export (positive)", Edition2018, false, reverse, 100},
		{"2023 DER forward is export (positive)", Edition2023, true, forward, 100},
		{"2023 DER reverse is import (negative)", Edition2023, true, reverse, -100},
		{"2023 non-DER forward keeps the 2018 mapping (negative)", Edition2023, false, forward, -100},
		{"2023 non-DER reverse keeps the 2018 mapping (positive)", Edition2023, false, reverse, 100},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A negative raw value throughout: proves abs(value) still
			// applies before the sign under every edition and isDER
			// combination (#715 fix round 1 item 2's rule, restated by
			// fix round 3 item 2), since a mapping that forgot to take
			// the magnitude would carry the raw sign through instead of
			// the edition-declared one.
			reading := typedReading("series", 100, sep2.UomWatts, tc.flow, -100)

			var out FleetDeviceMeasurements
			considerMeasurement(&out, reading, tc.edition, tc.isDER)

			if out.P == nil || out.P.Value != tc.want {
				t.Errorf("Measurements.P = %+v, want value %v", out.P, tc.want)
			}
		})
	}
}

// --- #715 fix round 4 item 2: a future-dated client-set readingTime must
// --- never read as negative age. -----------------------------------------

// TestClampedAge_FutureDatedIsFlooredAtZero proves the floor mechanism
// itself: clientClockStale's boolean result is identical whether a future
// date floors to 0 or stays negative (both fail the staleness comparison),
// so only a direct assertion on the age value can distinguish the two.
func TestClampedAge_FutureDatedIsFlooredAtZero(t *testing.T) {
	t.Parallel()
	now := int64(1000)
	future := int64(5000) // 4000 seconds in the future
	if got := clampedAge(future, now); got != 0 {
		t.Errorf("clampedAge(future, now) = %d, want 0 (floored, not -4000)", got)
	}
}

// TestClampedAge_PastIsUnaffected is the control: an ordinary past
// readingTime is not floored, so clampedAge(future,...) = 0 above is not
// merely clampedAge always returning 0.
func TestClampedAge_PastIsUnaffected(t *testing.T) {
	t.Parallel()
	now := int64(1000)
	past := int64(400)
	if got := clampedAge(past, now); got != 600 {
		t.Errorf("clampedAge(past, now) = %d, want 600", got)
	}
}

// TestAccumulateRollup_StatusDatedYear2100IsNotStaleAndConnectedWorks pins
// the end-to-end outcome the design names: a DERStatus dated in the future
// (here, year 2100) reads as not stale, and the Connected bit is still
// honored normally - the future date does not corrupt anything downstream
// of the staleness check.
func TestAccumulateRollup_StatusDatedYear2100IsNotStaleAndConnectedWorks(t *testing.T) {
	t.Parallel()
	now := int64(1732900000)      // an ordinary "now", late 2024
	year2100 := int64(4102444800) // 2100-01-01T00:00:00Z

	dev := FleetDevice{Status: &FleetDeviceStatus{Connected: boolPtr(true), ReadingTime: year2100}}
	var rollup FleetRollup
	accumulateRollup(&rollup, dev, now)

	if rollup.Stale != 0 {
		t.Errorf("Stale = %d, want 0", rollup.Stale)
	}
	if rollup.Connected != 1 {
		t.Errorf("Connected = %d, want 1", rollup.Connected)
	}
}

// --- #715 fix round 4 item 3: Q's staleness must be judged independently of
// --- P and of the device's DERStatus-derived staleness, with its own
// --- ">"/"" >= "" boundary pinned. ---------------------------------------

// TestMeasurementStale_Boundary pins measurementStale's own ">" boundary
// directly: TestStaleness_BothSidesOfBoundary above pins the SAME boundary
// shape for the unrelated clientClockStale/deviceStale path, and a ">="
// mutant in measurementStale specifically survives unless this function is
// exercised in isolation.
func TestMeasurementStale_Boundary(t *testing.T) {
	t.Parallel()
	now := int64(100000)

	t.Run("exactly at the boundary is not stale", func(t *testing.T) {
		m := &FleetMeasurement{ReadingTime: now - staleAfterSeconds}
		if measurementStale(m, now) {
			t.Error("measurementStale at exactly staleAfterSeconds = true, want false")
		}
	})

	t.Run("one second past the boundary is stale", func(t *testing.T) {
		m := &FleetMeasurement{ReadingTime: now - staleAfterSeconds - 1}
		if !measurementStale(m, now) {
			t.Error("measurementStale one second past staleAfterSeconds = false, want true")
		}
	})
}

// TestAccumulateRollup_QStalenessIndependentOfPAndDeviceStatus gives Q a
// staleness outcome that disagrees with BOTH P's and deviceStale's, so a
// mutant that judges Q by either of those signals instead of Q's own
// FleetMeasurement.ReadingTime fails here even though it might pass a test
// where all three happen to agree.
func TestAccumulateRollup_QStalenessIndependentOfPAndDeviceStatus(t *testing.T) {
	t.Parallel()
	now := int64(1000000)
	fresh := now
	oldTime := now - staleAfterSeconds - 1

	dev := FleetDevice{
		// deviceStale: stale (disagrees with Q, which is fresh).
		Status: &FleetDeviceStatus{ReadingTime: oldTime},
		Measurements: FleetDeviceMeasurements{
			// P: stale (disagrees with Q, which is fresh).
			P: &FleetMeasurement{Value: 111, ReadingTime: oldTime},
			// Q: fresh - the only one of the three that is.
			Q: &FleetMeasurement{Value: 222, ReadingTime: fresh},
		},
	}

	var rollup FleetRollup
	accumulateRollup(&rollup, dev, now)

	if rollup.Q.Sum != 222 || rollup.Q.Stale != 0 {
		t.Errorf("Q = %+v, want Sum 222, Stale 0: Q is fresh by its own clock even though P and the device status are both stale", rollup.Q)
	}
	if rollup.P.Sum != 0 || rollup.P.Stale != 1 {
		t.Errorf("P = %+v, want Sum 0, Stale 1 (control: P really is stale here, unlike Q)", rollup.P)
	}
}

// --- #715 fix round 4 item 4: edition/isDER through deviceMeasurements end
// --- to end, with isDER derived from a stored mirror's RoleFlags bit rather
// --- than passed as a literal to considerMeasurement directly. --------------

// TestDeviceMeasurements_EditionAndIsDERFromRoleFlags drives
// (*AdminFleetHandler).deviceMeasurements through a real stored
// MirrorUsagePoint, table-driven over both editions and three RoleFlags
// shapes, to kill four specific mutants at the isDER call site
// (admin_fleet.go:422) and the flip condition (admin_fleet.go's
// considerMeasurement):
//   - "flipped := isDER" (drops the edition check): the "2018, isDER bit
//     set" case would incorrectly flip.
//   - "isDER := false" (hardcoded): the "2023, isDER bit set" case would
//     fail to flip.
//   - "isDER := true" (hardcoded): the "2023, isDER bit clear" case would
//     incorrectly flip.
//   - "roleFlagIsDER = 1 << 2" (wrong bit): the "2023, bit 2 set, bit 3
//     clear" case would incorrectly flip, since bit 2 is isPEV, not isDER.
func TestDeviceMeasurements_EditionAndIsDERFromRoleFlags(t *testing.T) {
	t.Parallel()
	forward := f8(sep2.FlowDirectionForward)

	cases := []struct {
		name      string
		edition   SEP2Edition
		roleFlags sep2.RoleFlagsValue
		want      float64 // Forward, magnitude 100: -100 unflipped, +100 flipped
	}{
		{"2018, isDER bit (3) set: no flip", Edition2018, 1 << 3, -100},
		{"2023, isDER bit (3) set: flips", Edition2023, 1 << 3, 100},
		{"2023, isDER bit (3) clear: no flip", Edition2023, 0, -100},
		{"2023, bit 2 set (isPEV, not isDER), bit 3 clear: no flip", Edition2023, 1 << 2, -100},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mups := memory.NewStore[sep2.MirrorUsagePoint]()
			mupID := fmt.Sprintf("mup-%d", i)
			mup := sep2.MirrorUsagePoint{
				Resource:   sep2.Resource{Href: "/mup/" + mupID},
				DeviceLFDI: fleetTestLFDI,
				RoleFlags:  tc.roleFlags,
				MirrorMeterReading: []sep2.MirrorMeterReading{
					typedReading("series", 100, sep2.UomWatts, forward, 100),
				},
			}
			if err := mups.Create(context.Background(), mupID, mup); err != nil {
				t.Fatalf("MirrorUsagePoints.Create: %v", err)
			}
			h := &AdminFleetHandler{MirrorUsagePoints: mups, Edition: tc.edition}

			got, err := h.deviceMeasurements(context.Background(), fleetTestLFDI)
			if err != nil {
				t.Fatalf("deviceMeasurements: %v", err)
			}
			if got.P == nil || got.P.Value != tc.want {
				t.Errorf("Measurements.P = %+v, want value %v", got.P, tc.want)
			}
		})
	}
}

// directionRollup folds one device per entry into a rollup, each carrying a
// single reading of the given unit and flow direction, all fresh at now.
func directionRollup(uom uint8, flows ...*uint8) FleetRollup {
	const now = int64(1000)
	var rollup FleetRollup
	for _, flow := range flows {
		var m FleetDeviceMeasurements
		r := readingWithFlow(uom, flow, 100)
		r.LastUpdateTime = now
		considerMeasurement(&m, r, Edition2018, false)
		accumulateRollup(&rollup, FleetDevice{Measurements: m}, now)
	}
	return rollup
}

// TestFleetRollup_DirectionUnknownFlag is #733: a sum still totals every
// contributing reading, and is flagged only when one of them had no known
// flowDirection, in either order and for P and Q alike. Forward and Reverse
// both count as known; Net (4) stays flagged because its sign frame is not
// fixed by the export-positive convention here.
func TestFleetRollup_DirectionUnknownFlag(t *testing.T) {
	t.Parallel()
	forward := f8(sep2.FlowDirectionForward)
	reverse := f8(sep2.FlowDirectionReverse)
	cases := []struct {
		name     string
		flows    []*uint8
		wantSum  float64
		wantFlag bool
	}{
		{"reverse only", []*uint8{reverse, reverse}, 200, false},
		{"forward only", []*uint8{forward, forward}, -200, false},
		{"forward and reverse", []*uint8{forward, reverse}, 0, false},
		{"undirected last", []*uint8{reverse, nil}, 200, true},
		{"undirected first", []*uint8{nil, reverse}, 200, true},
		{"undirected only", []*uint8{nil}, 100, true},
		{"unrecognized code", []*uint8{reverse, f8(12)}, 200, true},
		{"net stays flagged", []*uint8{reverse, f8(4)}, 200, true},
	}
	for _, tc := range cases {
		for _, q := range []struct {
			name  string
			uom   uint8
			pick  func(FleetRollup) FleetSum
			other func(FleetRollup) FleetSum
		}{
			{"P", sep2.UomWatts, func(r FleetRollup) FleetSum { return r.P }, func(r FleetRollup) FleetSum { return r.Q }},
			{"Q", sep2.UomVars, func(r FleetRollup) FleetSum { return r.Q }, func(r FleetRollup) FleetSum { return r.P }},
		} {
			rollup := directionRollup(q.uom, tc.flows...)
			got := q.pick(rollup)
			if got.Sum != tc.wantSum || got.DirectionUnknown != tc.wantFlag {
				t.Errorf("%s %s: sum = %+v, want Sum %v DirectionUnknown %v", tc.name, q.name, got, tc.wantSum, tc.wantFlag)
			}
			if o := q.other(rollup); o.DirectionUnknown {
				t.Errorf("%s %s: the other power sum must not be flagged: %+v", tc.name, q.name, o)
			}
			if rollup.StatWAvail.DirectionUnknown || rollup.StatVarAvail.DirectionUnknown {
				t.Errorf("%s %s: capacity sums must never be flagged: %+v %+v", tc.name, q.name, rollup.StatWAvail, rollup.StatVarAvail)
			}
		}
	}
}

// TestFleetRollup_CapacitySumsNeverFlagged feeds availability values, which
// contribute to the capacity sums, and requires both to stay unflagged.
func TestFleetRollup_CapacitySumsNeverFlagged(t *testing.T) {
	t.Parallel()
	w, v := 5000.0, 2000.0
	var rollup FleetRollup
	accumulateRollup(&rollup, FleetDevice{Availability: &FleetDeviceAvailability{StatWAvail: &w, StatVarAvail: &v, ReadingTime: 1000}}, 1000)
	if rollup.StatWAvail.Sum != 5000 || rollup.StatVarAvail.Sum != 2000 {
		t.Fatalf("capacity sums = %+v %+v, want 5000 and 2000 contributing", rollup.StatWAvail, rollup.StatVarAvail)
	}
	if rollup.StatWAvail.DirectionUnknown || rollup.StatVarAvail.DirectionUnknown {
		t.Errorf("capacity sums flagged: %+v %+v", rollup.StatWAvail, rollup.StatVarAvail)
	}
}

// TestConsiderMeasurement_OnlyPowerReadingsCarryDirectionFlag: V and f have
// no flow direction, so their readings are never marked unknown.
func TestConsiderMeasurement_OnlyPowerReadingsCarryDirectionFlag(t *testing.T) {
	t.Parallel()
	var out FleetDeviceMeasurements
	considerMeasurement(&out, readingWithFlow(sep2.UomVolts, nil, 240), Edition2018, false)
	considerMeasurement(&out, readingWithFlow(uomHertz, nil, 60), Edition2018, false)
	considerMeasurement(&out, readingWithFlow(sep2.UomWatts, nil, 5), Edition2018, false)
	if out.V == nil || out.V.DirectionUnknown || out.F == nil || out.F.DirectionUnknown {
		t.Errorf("V = %+v, F = %+v, want both present and unflagged", out.V, out.F)
	}
	if out.P == nil || !out.P.DirectionUnknown {
		t.Errorf("P = %+v, want flagged (control)", out.P)
	}
}

// TestFleetRollup_MixedSumToZeroIsFlagged is the case a reader must never
// take for a fleet at zero: Reverse 100 plus an undirected -100 sums to 0.
func TestFleetRollup_MixedSumToZeroIsFlagged(t *testing.T) {
	t.Parallel()
	const now = int64(1000)
	var rollup FleetRollup
	for _, r := range []sep2.MirrorMeterReading{
		readingWithFlow(sep2.UomWatts, f8(sep2.FlowDirectionReverse), 100),
		readingWithFlow(sep2.UomWatts, nil, -100),
	} {
		r.LastUpdateTime = now
		var m FleetDeviceMeasurements
		considerMeasurement(&m, r, Edition2018, false)
		accumulateRollup(&rollup, FleetDevice{Measurements: m}, now)
	}
	if rollup.P.Sum != 0 || !rollup.P.DirectionUnknown {
		t.Errorf("P = %+v, want Sum 0 with DirectionUnknown true", rollup.P)
	}
	raw, err := json.Marshal(rollup.P)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"directionUnknown":true`) {
		t.Errorf("serialized = %s, want directionUnknown:true", raw)
	}
}

// TestFleetRollup_StaleUndirectedReadingDoesNotFlagSum: a stale reading is
// excluded from Sum, so its missing direction cannot taint the sum.
func TestFleetRollup_StaleUndirectedReadingDoesNotFlagSum(t *testing.T) {
	t.Parallel()
	now := int64(100000)
	var m FleetDeviceMeasurements
	r := readingWithFlow(sep2.UomWatts, nil, 100)
	r.LastUpdateTime = 1
	considerMeasurement(&m, r, Edition2018, false)
	var rollup FleetRollup
	accumulateRollup(&rollup, FleetDevice{Measurements: m}, now)
	if rollup.P.Stale != 1 || rollup.P.Sum != 0 || rollup.P.DirectionUnknown {
		t.Errorf("P = %+v, want Stale 1, Sum 0, DirectionUnknown false", rollup.P)
	}
}
