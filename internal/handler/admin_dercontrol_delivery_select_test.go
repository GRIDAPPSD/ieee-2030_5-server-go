package handler_test

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

// RoleFlagsType bits: isDER (3) and isPremisesAggregationPoint (1).
const (
	roleIsDER   = sep2.RoleFlagsValue(1 << 3)
	roleIsPremA = sep2.RoleFlagsValue(1 << 1)
)

// reading builds one mirror reading; nil fields are left absent.
type reading struct {
	mrid   string
	at     int64
	value  int64
	uom    uint8
	dir    *uint8
	qual   *uint8
	mult   *int8
	period *sep2.DateTimeInterval
}

func (r reading) mmr() sep2.MirrorMeterReading {
	uom := r.uom
	if uom == 0 {
		uom = sep2.UomWatts
	}
	v := r.value
	return sep2.MirrorMeterReading{
		MRID:           r.mrid,
		LastUpdateTime: r.at,
		Reading:        &sep2.Reading{Value: &v, TimePeriod: r.period},
		ReadingType:    &sep2.ReadingType{Uom: &uom, FlowDirection: r.dir, DataQualifier: r.qual, PowerOfTenMultiplier: r.mult},
	}
}

// seedReadings stores a mirror for lfdi with the given role flags and
// postRate (nil leaves it absent) and its out-of-band readings.
func (d *deliveryHarness) seedReadings(t *testing.T, id, lfdi string, roles sep2.RoleFlagsValue, postRate *uint32, rs ...reading) {
	t.Helper()
	ctx := context.Background()
	mup := sep2.MirrorUsagePoint{MRID: "MUP" + id, DeviceLFDI: lfdi, PostRate: postRate, RoleFlags: roles}
	mup.Href = "/mup/" + id
	if err := d.mups.Create(ctx, id, mup); err != nil {
		t.Fatal(err)
	}
	for i, r := range rs {
		if err := d.mmrs.Create(ctx, id, fmt.Sprint(i), r.mmr()); err != nil {
			t.Fatal(err)
		}
	}
}

func dq(v uint8) *uint8 { return &v }

func (d *deliveryHarness) deliveryOf(t *testing.T, mrid string) *handler.DERControlDelivery {
	t.Helper()
	got, body := d.list(t)
	del := got[mrid].Delivery
	if del == nil {
		t.Fatalf("control %s has no delivery: %s", mrid, body)
	}
	return del
}

// Item 1: only DER mirrors count; a premises mirror's net power is not the
// DER's output, so it is ignored even where it alone covers a second.
func TestDERControlList_DeliveryCountsOnlyDERMirrors(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "der", dcLFDI, roleIsDER, ptrU32(900), reading{mrid: "P", at: b, value: 3600, dir: reverseDir()})
	d.seedReadings(t, "site", dcLFDI, roleIsPremA, ptrU32(900), reading{mrid: "P", at: b + 100, value: 7200, dir: reverseDir()})
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if del.CoveredSeconds != 900 || del.Readings != 1 {
		t.Fatalf("delivery = %+v, want 900 s from the DER mirror only", *del)
	}
	wantWh(t, del.DeliveredWh, 900)
}

// Item 1: Maximum and Minimum series are ignored, as is reactive power.
func TestDERControlList_DeliveryIgnoresMaxMinAndVAr(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900),
		reading{mrid: "MAX", at: b, value: 9000, dir: reverseDir(), qual: dq(8)},
		reading{mrid: "MIN", at: b, value: 9000, dir: reverseDir(), qual: dq(9)},
		reading{mrid: "VAR", at: b, value: 9000, dir: reverseDir(), uom: sep2.UomVars},
	)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if del.DeliveredWh != nil || del.CoveredSeconds != 0 || del.Readings != 0 {
		t.Fatalf("delivery = %+v, want a null figure: Maximum, Minimum and VAr are not delivery", *del)
	}
}

// Item 1: parallel series are summed, on one mirror (per phase) and across
// two DER mirrors, rather than one standing for the whole.
func TestDERControlList_DeliverySumsParallelSeries(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900),
		reading{mrid: "PA", at: b, value: 1800, dir: reverseDir()},
		reading{mrid: "PB", at: b, value: 1800, dir: reverseDir()},
	)
	d.seedReadings(t, "2", dcLFDI, roleIsDER, ptrU32(900), reading{mrid: "P", at: b, value: 3600, dir: reverseDir()})
	mrid := d.seedControl(t, "e", b, 900, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if del.CoveredSeconds != 900 || del.Readings != 3 {
		t.Fatalf("delivery = %+v, want 900 covered seconds from 3 readings", *del)
	}
	// 1800 + 1800 + 3600 W over 900 s.
	wantWh(t, del.DeliveredWh, 1800)
}

// Item 2: the hold is capped at 900 s whatever postRate a device declares.
func TestDERControlList_DeliveryHoldCappedAt900(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(4294967295), reading{mrid: "P", at: b, value: 3600, dir: reverseDir()})
	mrid := d.seedControl(t, "e", b, 86400, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if del.CoveredSeconds != 900 {
		t.Fatalf("covered = %d, want 900", del.CoveredSeconds)
	}
	wantWh(t, del.DeliveredWh, 900)
}

// Item 2: an Average reading with no timePeriod covers the posting interval
// before its receipt, bounded by the previous reading of its series.
func TestDERControlList_DeliveryAverageCoversBeforeReceipt(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	avg := dq(2)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300),
		reading{mrid: "P", at: b + 300, value: 3600, dir: reverseDir(), qual: avg},
		reading{mrid: "P", at: b + 400, value: 7200, dir: reverseDir(), qual: avg},
	)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	// [b, b+300) at 3600 W, then [b+300, b+400) at 7200 W.
	if del.CoveredSeconds != 400 || del.Readings != 2 {
		t.Fatalf("delivery = %+v, want 400 covered seconds before the receipts", *del)
	}
	wantWh(t, del.DeliveredWh, 300+200)
}

// Item 3: under 2023 a negative value under Forward or Reverse is flagged,
// not folded into a magnitude, on the delivery figure.
func TestDERControlList_DeliveryFlagsNegativeDirectedValue(t *testing.T) {
	d := newDeliveryHarness(t)
	d.h.Edition = handler.Edition2023
	b := deliveryBase
	fwd := sep2.FlowDirectionForward
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(100),
		reading{mrid: "P", at: b, value: 3600, dir: &fwd},
		reading{mrid: "Q", at: b + 200, value: -3600, dir: reverseDir()},
		reading{mrid: "R", at: b + 400, value: -3600, dir: &fwd},
	)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if !del.DirectionUnknown || del.CoveredSeconds != 100 || del.Readings != 1 {
		t.Fatalf("delivery = %+v, want the two negative readings flagged and unsummed", *del)
	}
	wantWh(t, del.DeliveredWh, 100)
}

// Item 5: the power-of-ten multiplier scales the delivered figure.
func TestDERControlList_DeliveryAppliesMultiplier(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	m := int8(3)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900), reading{mrid: "P", at: b, value: 4, dir: reverseDir(), mult: &m})
	mrid := d.seedControl(t, "e", b, 900, dercontrol.LifecycleRecord{})

	wantWh(t, d.deliveryOf(t, mrid).DeliveredWh, 4000*900/3600.0)
}

// Item 5: a running control's window ends at now, on the route.
func TestDERControlList_DeliveryRunningControlClippedByNow(t *testing.T) {
	d := newDeliveryHarness(t)
	start := sep2time.Now().Unix() - 100
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900), reading{mrid: "P", at: start, value: 3600, dir: reverseDir()})
	mrid := d.seedControl(t, "e", start, 1000, dercontrol.LifecycleRecord{})

	before := sep2time.Now().Unix()
	del := d.deliveryOf(t, mrid)
	after := sep2time.Now().Unix()
	if del.WindowStart != start || del.WindowEnd < before || del.WindowEnd > after || del.CoveredSeconds != del.WindowEnd-start {
		t.Fatalf("delivery = %+v, want window [%d, now in [%d,%d]) fully covered", *del, start, before, after)
	}
	wantWh(t, del.DeliveredWh, float64(del.WindowEnd-start))
}

// Item 5: an instantaneous reading holds only until the next reading of its
// series, even when that next reading covers nothing.
func TestDERControlList_DeliveryHoldStopsAtNextReading(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300),
		reading{mrid: "P", at: b, value: 3600, dir: reverseDir()},
		reading{mrid: "P", at: b + 100, value: 3600},
	)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if del.CoveredSeconds != 100 || !del.DirectionUnknown {
		t.Fatalf("delivery = %+v, want 100 s and the directionless follow-up flagged", *del)
	}
}

// Item 5: postRate 0 is treated as absent, giving the 300 s default hold.
func TestDERControlList_DeliveryPostRateZeroIsDefault(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(0), reading{mrid: "P", at: b, value: 3600, dir: reverseDir()})
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	if del := d.deliveryOf(t, mrid); del.CoveredSeconds != 300 {
		t.Fatalf("covered = %d, want 300", del.CoveredSeconds)
	}
}

// Item 5: an Average reading whose timePeriod has zero duration has no
// period, so it covers the posting interval before receipt.
func TestDERControlList_DeliveryZeroDurationPeriodIsNoPeriod(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(200),
		reading{mrid: "P", at: b + 500, value: 3600, dir: reverseDir(), qual: dq(2), period: &sep2.DateTimeInterval{Start: b + 10, Duration: 0}})
	mrid := d.seedControl(t, "e", b, 600, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if del.CoveredSeconds != 200 {
		t.Fatalf("covered = %d, want 200", del.CoveredSeconds)
	}
	wantWh(t, del.DeliveredWh, 200)
}

// Item 5: a directionless reading received exactly at the window end covers
// nothing in the window and does not flag it.
func TestDERControlList_DeliveryDirectionlessAtWindowEndIsNotFlagged(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300),
		reading{mrid: "P", at: b, value: 3600, dir: reverseDir()},
		reading{mrid: "Q", at: b + 1000, value: 3600},
	)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	if del := d.deliveryOf(t, mrid); del.DirectionUnknown {
		t.Fatalf("delivery = %+v, want no flag from a reading outside the window", *del)
	}
}

// Item 5: two overlapping readings of one series received at the same second
// resolve to the later-stored one, every time.
func TestDERControlList_DeliveryEqualReceiptTimes(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	avg := dq(2)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300),
		reading{mrid: "P", at: b + 500, value: 1000, dir: reverseDir(), qual: avg, period: &sep2.DateTimeInterval{Start: b, Duration: 360}},
		reading{mrid: "P", at: b + 500, value: 3600, dir: reverseDir(), qual: avg, period: &sep2.DateTimeInterval{Start: b, Duration: 360}},
	)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if del.CoveredSeconds != 360 || del.Readings != 1 {
		t.Fatalf("delivery = %+v, want 360 s from one reading", *del)
	}
	wantWh(t, del.DeliveredWh, 360)
}

// Item 6: a device with no controls never reads the mirror stores, so a
// mirror store failure cannot fail its empty list.
func TestDERControlList_NoControlsReadsNoMirrors(t *testing.T) {
	d := newDeliveryHarness(t)
	d.h.MirrorUsagePoints = failingMUPs{}
	w := d.do(t, http.MethodGet, "/api/der/controls?device=0", "")
	if w.Code != http.StatusOK || !containsAll(w.Body.String(), `"controls":[]`) {
		t.Fatalf("list = %d %s, want 200 with no controls", w.Code, w.Body.String())
	}
}

// Item 3: the fleet route uses the same mapping, so under 2023 a negative
// directed P reading is served as its raw value with directionUnknown set.
func TestHandleListFleets_NegativeDirectedValueIsFlagged(t *testing.T) {
	f := newFleetFixture(t)
	f.assign(fleetAggregatorLFDI, fleetDeviceALFDI)
	f.postInlineReading("mup-a", fleetDeviceALFDI, sep2.MirrorMeterReading{
		MRID: "a-p", LastUpdateTime: handlerTestNow(),
		ReadingType: &sep2.ReadingType{Uom: u8(sep2.UomWatts), FlowDirection: u8(sep2.FlowDirectionReverse), PowerOfTenMultiplier: i8(0)},
		Reading:     &sep2.Reading{Value: i64(-500)},
	})
	h := f.handler()
	h.Edition = handler.Edition2023
	w := httptest.NewRecorder()
	handler.HandleListFleets(h)(w, httptest.NewRequest(http.MethodGet, "/api/derms/fleets", nil))
	want := `"p":{"value":-500,`
	if !containsAll(w.Body.String(), want, `"directionUnknown":true`) {
		t.Fatalf("fleet body lacks %s with directionUnknown: %s", want, w.Body.String())
	}
	dev := findDevice(t, fetchFleets(t, h)[0], fleetDeviceALFDI)
	if dev.Measurements.P == nil || dev.Measurements.P.Value != -500 || !dev.Measurements.P.DirectionUnknown {
		t.Fatalf("P = %+v, want -500 flagged", dev.Measurements.P)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// Item 4: averageW is the delivered energy over the covered seconds, served
// beside deliveredWh, so a partly covered window reads as a power.
func TestDERControlList_DeliveryAverageW(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900), reading{mrid: "P", at: b, value: 3600, dir: reverseDir()})
	d.seedControl(t, "e", b, 1800, dercontrol.LifecycleRecord{})

	_, body := d.list(t)
	want := `"deliveredWh":900,"averageW":3600,"coveredSeconds":900`
	if !strings.Contains(body, want) {
		t.Fatalf("body lacks %s: %s", want, body)
	}
}

// Item 6: a mirror store failure logs the delivery route, not the fleet
// route, and the 500's audit line names both the step and the route.
func TestDERControlList_DeliveryStoreErrorLogsRoute(t *testing.T) {
	var std strings.Builder
	prev := log.Writer()
	log.SetOutput(&std)
	t.Cleanup(func() { log.SetOutput(prev) })

	d := newDeliveryHarness(t)
	d.seedControl(t, "e", deliveryBase, 1000, dercontrol.LifecycleRecord{})
	d.h.MirrorUsagePoints = failingMUPs{}
	assertRefusal(t, d.do(t, http.MethodGet, "/api/der/controls?device=0", ""), http.StatusInternalServerError, "internal error")

	if got := std.String(); !strings.Contains(got, "admin GET /api/der/controls: MirrorUsagePoints.List") {
		t.Errorf("helper log = %q, want the delivery route named", got)
	}
	if got := d.logs.String(); !strings.Contains(got, `step="list mirror readings"`) || !strings.Contains(got, `route="GET /api/der/controls"`) {
		t.Errorf("audit log = %q, want the step and the route", got)
	}
}
