package handler

import (
	"fmt"
	"math"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

func u8(v uint8) *uint8    { return &v }
func u32(v uint32) *uint32 { return &v }
func i64(v int64) *int64   { return &v }

// wReading is a real-power reading received at t. dir and qualifier may be nil.
func wReading(mrid string, t, watts int64, dir, qualifier *uint8, period *sep2.DateTimeInterval) sep2.MirrorMeterReading {
	return sep2.MirrorMeterReading{
		MRID:           mrid,
		LastUpdateTime: t,
		Reading:        &sep2.Reading{Value: i64(watts), TimePeriod: period},
		ReadingType:    &sep2.ReadingType{Uom: u8(sep2.UomWatts), FlowDirection: dir, DataQualifier: qualifier},
	}
}

var reverse = u8(sep2.FlowDirectionReverse) // export under 2018

func approx(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = null, want %v", name, want)
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Fatalf("%s = %v, want %v", name, *got, want)
	}
}

// Criterion: an Average reading integrates over its timePeriod. Without one
// (intervalLength is not decoded by the vendored core) it covers the postRate
// interval before its receipt.
func TestDelivery_AverageIntegratesOverTimePeriod(t *testing.T) {
	avg := u8(dataQualifierAverage)
	t.Run("timePeriod", func(t *testing.T) {
		m := []deviceMirror{{isDER: true, postRate: u32(60), readings: []sep2.MirrorMeterReading{
			wReading("P", 1600, 1200, reverse, avg, &sep2.DateTimeInterval{Start: 1200, Duration: 300}),
		}}}
		d := newDelivery("L", m, Edition2018, 1000, 2000)
		if d.CoveredSeconds != 300 || d.Readings != 1 || d.NewestReadingTime == nil || *d.NewestReadingTime != 1600 {
			t.Fatalf("delivery = %+v, want 300 covered seconds from 1 reading received at 1600", d)
		}
		approx(t, "deliveredWh", d.DeliveredWh, 1200*300.0/3600)
	})
	t.Run("no timePeriod covers postRate before receipt", func(t *testing.T) {
		m := []deviceMirror{{isDER: true, postRate: u32(60), readings: []sep2.MirrorMeterReading{
			wReading("P", 1500, 1200, reverse, avg, nil),
		}}}
		d := newDelivery("L", m, Edition2018, 1000, 1470)
		if d.CoveredSeconds != 30 {
			t.Fatalf("covered = %d, want 30 of [1440,1500) inside the window", d.CoveredSeconds)
		}
		approx(t, "deliveredWh", d.DeliveredWh, 1200*30.0/3600)
		approx(t, "averageW", d.AverageW, 1200)
	})
	t.Run("a non-Average reading with a timePeriod holds", func(t *testing.T) {
		m := []deviceMirror{{isDER: true, postRate: u32(60), readings: []sep2.MirrorMeterReading{
			wReading("P", 1500, 1200, reverse, nil, &sep2.DateTimeInterval{Start: 1100, Duration: 300}),
		}}}
		d := newDelivery("L", m, Edition2018, 1000, 2000)
		if d.CoveredSeconds != 60 {
			t.Fatalf("covered = %d, want 60", d.CoveredSeconds)
		}
	})
}

// Criterion: seconds no reading covers are reported, never filled. A held
// reading stops at the next reading of its series or at postRate, and a
// mirror with no postRate holds for defaultHoldSeconds.
func TestDelivery_UncoveredSecondsAreNotFilled(t *testing.T) {
	m := []deviceMirror{{isDER: true, postRate: u32(100), readings: []sep2.MirrorMeterReading{
		wReading("P", 1500, 1000, reverse, nil, nil),
		wReading("P", 1000, 500, reverse, nil, nil),
		wReading("P", 1550, 2000, reverse, nil, nil),
	}}}
	d := newDelivery("L", m, Edition2018, 1000, 2000)
	// [1000,1100) at 500 W, [1500,1550) at 1000 W, [1550,1650) at 2000 W.
	if d.WindowStart != 1000 || d.WindowEnd != 2000 || d.CoveredSeconds != 250 || d.Readings != 3 {
		t.Fatalf("delivery = %+v, want window [1000,2000), 250 covered, 3 readings", d)
	}
	approx(t, "deliveredWh", d.DeliveredWh, (500*100+1000*50+2000*100)/3600.0)

	d = newDelivery("L", []deviceMirror{{isDER: true, readings: []sep2.MirrorMeterReading{wReading("P", 1000, 3600, reverse, nil, nil)}}}, Edition2018, 0, 5000)
	if d.CoveredSeconds != defaultHoldSeconds {
		t.Fatalf("covered = %d without postRate, want %d", d.CoveredSeconds, defaultHoldSeconds)
	}
	approx(t, "deliveredWh", d.DeliveredWh, defaultHoldSeconds)

	none := newDelivery("L", nil, Edition2018, 1000, 2000)
	if none.DeliveredWh != nil || none.CoveredSeconds != 0 || none.Readings != 0 || none.NewestReadingTime != nil {
		t.Fatalf("no readings = %+v, want a null figure", none)
	}
}

// Overlapping spans of one series count each second once, using the newest
// receipt; spans of different series (mirrors or mRIDs) add, over the seconds
// every series covers.
func TestDelivery_OverlapWithinSeriesAndSumAcross(t *testing.T) {
	avg := u8(dataQualifierAverage)
	t.Run("one series", func(t *testing.T) {
		m := []deviceMirror{{isDER: true, readings: []sep2.MirrorMeterReading{
			wReading("A", 1300, 100, reverse, avg, &sep2.DateTimeInterval{Start: 1000, Duration: 300}),
			wReading("A", 1400, 400, reverse, avg, &sep2.DateTimeInterval{Start: 1200, Duration: 200}),
		}}}
		d := newDelivery("L", m, Edition2018, 0, 5000)
		if d.CoveredSeconds != 400 || d.Readings != 2 || *d.NewestReadingTime != 1400 {
			t.Fatalf("delivery = %+v, want 400 covered by 2 readings, newest 1400", d)
		}
		approx(t, "deliveredWh", d.DeliveredWh, (100*200+400*200)/3600.0)
		approx(t, "averageW", d.AverageW, (100*200+400*200)/400.0)
	})
	t.Run("two mirrors", func(t *testing.T) {
		m := []deviceMirror{
			{isDER: true, readings: []sep2.MirrorMeterReading{wReading("A", 1300, 100, reverse, avg, &sep2.DateTimeInterval{Start: 1000, Duration: 300})}},
			{isDER: true, readings: []sep2.MirrorMeterReading{wReading("A", 1400, 400, reverse, avg, &sep2.DateTimeInterval{Start: 1200, Duration: 200})}},
		}
		d := newDelivery("L", m, Edition2018, 0, 5000)
		// Only [1200,1300) is covered by both mirrors.
		if d.CoveredSeconds != 100 || d.Readings != 2 {
			t.Fatalf("delivery = %+v, want 100 covered by 2 readings", d)
		}
		approx(t, "deliveredWh", d.DeliveredWh, (100+400)*100/3600.0)
	})
	t.Run("non-DER mirror ignored", func(t *testing.T) {
		m := []deviceMirror{{readings: []sep2.MirrorMeterReading{wReading("A", 1300, 100, reverse, avg, &sep2.DateTimeInterval{Start: 1000, Duration: 300})}}}
		if d := newDelivery("L", m, Edition2018, 0, 5000); d.DeliveredWh != nil || d.AverageW != nil {
			t.Fatalf("delivery = %+v, want null from a non-DER mirror", d)
		}
	})
}

// Criterion: a superseded or cancelled control stops at that time.
func TestEffectiveWindow(t *testing.T) {
	iv := sep2.DateTimeInterval{Start: 1000, Duration: 1000}
	for _, tc := range []struct {
		name       string
		lc         dercontrol.LifecycleRecord
		now        int64
		start, end int64
	}{
		{"ended", dercontrol.LifecycleRecord{}, 9000, 1000, 2000},
		{"running", dercontrol.LifecycleRecord{}, 1500, 1000, 1500},
		{"not started", dercontrol.LifecycleRecord{}, 500, 1000, 1000},
		{"superseded", dercontrol.LifecycleRecord{SupersededAt: i64(1400)}, 9000, 1000, 1400},
		{"cancelled", dercontrol.LifecycleRecord{CancelledAt: i64(1250)}, 9000, 1000, 1250},
		{"cancelled before supersede", dercontrol.LifecycleRecord{CancelledAt: i64(1100), SupersededAt: i64(1400)}, 9000, 1000, 1100},
		{"superseded before start", dercontrol.LifecycleRecord{SupersededAt: i64(800)}, 9000, 1000, 1000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e := effectiveWindow(iv, tc.lc, tc.now)
			if s != tc.start || e != tc.end {
				t.Fatalf("window = [%d,%d), want [%d,%d)", s, e, tc.start, tc.end)
			}
		})
	}
}

// Criterion: a reading with no direction is flagged and not summed, and its
// seconds are not covered.
func TestDelivery_DirectionlessReadingIsFlaggedNotSummed(t *testing.T) {
	m := []deviceMirror{{isDER: true, postRate: u32(100), readings: []sep2.MirrorMeterReading{
		wReading("P", 1000, 500, reverse, nil, nil),
		wReading("Q", 1200, 9000, nil, nil, nil),
	}}}
	d := newDelivery("L", m, Edition2018, 1000, 2000)
	if !d.DirectionUnknown || d.CoveredSeconds != 100 || d.Readings != 1 {
		t.Fatalf("delivery = %+v, want directionUnknown with only the directed 100 s covered", d)
	}
	approx(t, "deliveredWh", d.DeliveredWh, 500*100/3600.0)

	only := newDelivery("L", []deviceMirror{{isDER: true, readings: []sep2.MirrorMeterReading{wReading("Q", 1200, 9000, nil, nil, nil)}}}, Edition2018, 1000, 2000)
	if !only.DirectionUnknown || only.DeliveredWh != nil {
		t.Fatalf("delivery = %+v, want directionUnknown and a null figure", only)
	}

	outside := newDelivery("L", []deviceMirror{{isDER: true, readings: []sep2.MirrorMeterReading{wReading("Q", 5000, 9000, nil, nil, nil)}}}, Edition2018, 1000, 2000)
	if outside.DirectionUnknown {
		t.Fatalf("a directionless reading outside the window flagged the delivery: %+v", outside)
	}
}

// Criterion: the sign uses the fleet pane's mapping. Each case runs the same
// reading through considerMeasurement (the fleet route) and, for a DER
// mirror, newDelivery, and requires both to give the expected export-positive
// watts. A negative value under Forward or Reverse keeps its sign under 2018
// and is flagged under 2023, on both.
func TestDelivery_SignMatchesFleetMapping(t *testing.T) {
	forward, net := u8(sep2.FlowDirectionForward), u8(flowDirectionNet)
	for _, tc := range []struct {
		edition SEP2Edition
		isDER   bool
		dir     *uint8
		wire    int64
		want    float64
		flagged bool
	}{
		{Edition2018, false, forward, 360, -360, false},
		{Edition2018, false, reverse, 360, 360, false},
		{Edition2018, true, forward, 360, -360, false},
		{Edition2018, true, reverse, 360, 360, false},   // 2018 Table E.2: DER active power is Reverse
		{Edition2018, true, reverse, -360, -360, false}, // the sender's sign, kept under 2018
		{Edition2023, false, reverse, 360, 360, false},
		{Edition2023, true, forward, 360, 360, false},
		{Edition2023, true, forward, -360, -360, true},
		{Edition2023, true, reverse, 360, -360, false},
		{Edition2023, true, net, -360, -360, false},
	} {
		t.Run(fmt.Sprintf("%s der=%v dir=%d wire=%d", tc.edition, tc.isDER, *tc.dir, tc.wire), func(t *testing.T) {
			r := wReading("P", 1000, tc.wire, tc.dir, nil, nil)
			var fleet FleetDeviceMeasurements
			considerMeasurement(&fleet, r, tc.edition, tc.isDER)
			if fleet.P == nil || fleet.P.Value != tc.want || fleet.P.DirectionUnknown != tc.flagged {
				t.Fatalf("fleet P = %+v, want %v flagged=%v", fleet.P, tc.want, tc.flagged)
			}
			if !tc.isDER {
				return
			}
			// 900 s of hold makes Wh a quarter of W.
			d := newDelivery("L", []deviceMirror{{isDER: true, postRate: u32(900), readings: []sep2.MirrorMeterReading{r}}}, tc.edition, 1000, 1900)
			if tc.flagged {
				if d.DeliveredWh != nil || !d.DirectionUnknown {
					t.Fatalf("delivery = %+v, want a flagged null figure", d)
				}
				return
			}
			approx(t, "deliveredWh", d.DeliveredWh, tc.want/4)
		})
	}
}
