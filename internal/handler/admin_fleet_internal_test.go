package handler

import (
	"context"
	"errors"
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
				considerMeasurement(&out, readingWithFlow(uom.uom, tc.flow, tc.raw))
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
		considerMeasurement(&out, older)
		considerMeasurement(&out, newer)
		if out.P == nil || out.P.Value != 200 {
			t.Errorf("P = %+v, want value 200 (the newer reading)", out.P)
		}
	})

	t.Run("newer processed first", func(t *testing.T) {
		var out FleetDeviceMeasurements
		considerMeasurement(&out, newer)
		considerMeasurement(&out, older)
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
			considerMeasurement(&out, r)
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

// TestAccumulateRollup_StaleDeviceExcludedFromSums is #715 fix round 1 item
// 4: a stale device's measurements must not contribute to a fleet sum, and
// must be counted separately from "never reported".
func TestAccumulateRollup_StaleDeviceExcludedFromSums(t *testing.T) {
	t.Parallel()
	now := int64(100000)
	dev := FleetDevice{
		Status:       &FleetDeviceStatus{ReadingTime: now - staleAfterSeconds - 1},
		Measurements: FleetDeviceMeasurements{P: &FleetMeasurement{Value: 700, ReadingTime: now}},
	}

	var rollup FleetRollup
	accumulateRollup(&rollup, dev, now)

	if rollup.Stale != 1 {
		t.Errorf("Stale = %d, want 1", rollup.Stale)
	}
	if rollup.P.Sum != 0 {
		t.Errorf("P.Sum = %v, want 0: a stale device's P must not be summed", rollup.P.Sum)
	}
	if rollup.P.Stale != 1 {
		t.Errorf("P.Stale = %d, want 1", rollup.P.Stale)
	}
	if rollup.P.Unreported != 0 {
		t.Errorf("P.Unreported = %d, want 0: a stale device is not the same fact as an unreported one", rollup.P.Unreported)
	}
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

func TestLatestStatus_GenuineFailureIsLoggedAndAbsent(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{DERStatuses: erroringScopedReader[sep2.DERStatus]{err: errors.New("disk read failed")}}

	got := h.latestStatus(context.Background(), "3/1")
	if got != nil {
		t.Errorf("latestStatus = %+v, want nil: there is still nothing to report", got)
	}
	if !strings.Contains(buf.String(), "disk read failed") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

func TestLatestStatus_ErrNotFoundIsTheOrdinaryCaseAndSilent(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{DERStatuses: erroringScopedReader[sep2.DERStatus]{err: store.ErrNotFound}}

	got := h.latestStatus(context.Background(), "3/1")
	if got != nil {
		t.Errorf("latestStatus = %+v, want nil", got)
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none: a device with no DERStatus yet is the ordinary case, not a failure", buf.String())
	}
}

func TestLatestAvailability_GenuineFailureIsLoggedAndAbsent(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{DERAvailabilities: erroringScopedReader[sep2.DERAvailability]{err: errors.New("disk read failed")}}

	got := h.latestAvailability(context.Background(), "3/1")
	if got != nil {
		t.Errorf("latestAvailability = %+v, want nil", got)
	}
	if !strings.Contains(buf.String(), "disk read failed") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

func TestLatestAvailability_ErrNotFoundIsTheOrdinaryCaseAndSilent(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{DERAvailabilities: erroringScopedReader[sep2.DERAvailability]{err: store.ErrNotFound}}

	got := h.latestAvailability(context.Background(), "3/1")
	if got != nil {
		t.Errorf("latestAvailability = %+v, want nil", got)
	}
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none", buf.String())
	}
}

func TestBuildDevice_EndDevicesGenuineFailureIsLoggedAndAbsent(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{EndDevices: erroringEndDevices{err: errors.New("lookup backend down")}}

	fd := h.buildDevice(context.Background(), fleetTestLFDI)
	if fd.Status != nil || fd.Availability != nil {
		t.Errorf("buildDevice = %+v, want no status or availability", fd)
	}
	if !strings.Contains(buf.String(), "lookup backend down") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

func TestBuildDevice_EndDevicesErrNotFoundIsTheOrdinaryCaseAndSilent(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{EndDevices: erroringEndDevices{err: store.ErrNotFound}}

	h.buildDevice(context.Background(), fleetTestLFDI)
	if buf.Len() != 0 {
		t.Errorf("log output = %q, want none: a managed LFDI with no EndDevice record is the ordinary case", buf.String())
	}
}

func TestBuildDevice_DERsListFailureIsLogged(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{
		EndDevices: erroringEndDevices{}, // nil err: GetByLFDI succeeds with a zero-value EndDevice
		DERs:       erroringScopedReader[sep2.DER]{err: errors.New("der list backend down")},
	}

	fd := h.buildDevice(context.Background(), fleetTestLFDI)
	if fd.Status != nil || fd.Availability != nil {
		t.Errorf("buildDevice = %+v, want no status or availability", fd)
	}
	if !strings.Contains(buf.String(), "der list backend down") {
		t.Errorf("log output = %q, want it to name the failure: DERs.List never fails for an unknown parent, so any error here is genuine", buf.String())
	}
}

func TestDeviceMeasurements_MirrorUsagePointsListFailureIsLogged(t *testing.T) {
	buf := captureLog(t)
	h := &AdminFleetHandler{MirrorUsagePoints: erroringResourceReader[sep2.MirrorUsagePoint]{err: errors.New("mup list backend down")}}

	got := h.deviceMeasurements(context.Background(), fleetTestLFDI)
	if got.P != nil || got.Q != nil || got.V != nil || got.F != nil {
		t.Errorf("deviceMeasurements = %+v, want all nil", got)
	}
	if !strings.Contains(buf.String(), "mup list backend down") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

func TestDeviceMeasurements_MirrorMeterReadingsListFailureIsLogged(t *testing.T) {
	buf := captureLog(t)
	mups := memoryMirrorUsagePointsFor(t, fleetTestLFDI)
	h := &AdminFleetHandler{
		MirrorUsagePoints:   mups,
		MirrorMeterReadings: erroringScopedReader[sep2.MirrorMeterReading]{err: errors.New("mmr list backend down")},
	}

	h.deviceMeasurements(context.Background(), fleetTestLFDI)
	if !strings.Contains(buf.String(), "mmr list backend down") {
		t.Errorf("log output = %q, want it to name the failure", buf.String())
	}
}

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
