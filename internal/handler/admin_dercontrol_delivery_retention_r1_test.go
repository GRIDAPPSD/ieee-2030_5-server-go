package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mirrorretention"
)

// putMirror stores a DER mirror for device 0 with the given inline readings.
func (d *deliveryHarness) putMirror(t *testing.T, id string, postRate uint32, inline ...sep2.MirrorMeterReading) {
	t.Helper()
	mup := sep2.MirrorUsagePoint{MRID: "MUP" + id, DeviceLFDI: dcLFDI, PostRate: &postRate, RoleFlags: roleIsDER, MirrorMeterReading: inline}
	mup.Href = "/mup/" + id
	if err := d.mups.Create(context.Background(), id, mup); err != nil {
		t.Fatal(err)
	}
}

// putReading stores an out-of-band reading the way POST /mup/{id}/mr does.
func (d *deliveryHarness) putReading(t *testing.T, mupID string, nanos int64, mrid string, value *int64, rt *sep2.ReadingType) {
	t.Helper()
	id := fmt.Sprintf("%020d", nanos)
	r := sep2.MirrorMeterReading{MRID: mrid, LastUpdateTime: time.Unix(0, nanos).Unix(), ReadingType: rt}
	if value != nil {
		r.Reading = &sep2.Reading{Value: value}
	}
	r.Href = "/mup/" + mupID + "/mr/" + id
	if err := d.mmrs.Create(context.Background(), mupID, id, r); err != nil {
		t.Fatal(err)
	}
}

func exportWatts() *sep2.ReadingType {
	u := sep2.UomWatts
	return &sep2.ReadingType{Uom: &u, FlowDirection: reverseDir()}
}

// Fix round item 1: a mirror carrying many series at a conforming rate keeps
// every reading the time floor keeps. One W series and 17 others at 60 s
// under a 24 h control, swept at its end with the defaults.
func TestDERControlList_ManySeriesKeepFullCoverage(t *testing.T) {
	d := newDeliveryHarness(t)
	const day = int64(86400)
	start := deliveryBase
	d.putMirror(t, "1", 60)
	w := int64(3600)
	hz := int64(240)
	u := sep2.UomVolts
	n := 0
	for at := start; at < start+day; at += 60 {
		for s := range 18 {
			nanos := time.Unix(at, 0).UnixNano() + int64(s)
			if s == 0 {
				d.putReading(t, "1", nanos, "W", &w, exportWatts())
				n++
			} else {
				d.putReading(t, "1", nanos, fmt.Sprintf("V%02d", s), &hz, &sep2.ReadingType{Uom: &u})
			}
		}
	}
	mrid := d.seedControl(t, "e", start, uint32(day), dercontrol.LifecycleRecord{})
	ret := &mirrorretention.Retention{
		Readings:     d.mmrs,
		Mirrors:      d.mups,
		MaxAge:       config.DefaultMirrorReadingRetention,
		MaxPerSeries: config.DefaultMirrorReadingMaxPerSeries,
		Log:          slog.New(slog.DiscardHandler),
	}
	if removed, err := ret.Sweep(context.Background(), time.Unix(start+day, 0)); err != nil || removed != 0 {
		t.Errorf("Sweep = %d, %v, want nothing removed", removed, err)
	}
	del := d.deliveryOf(t, mrid)
	if del == nil || del.CoveredSeconds != day || del.Readings != n {
		t.Fatalf("delivery = %+v, want %d s covered by %d readings", del, day, n)
	}
}

// Fix round item 3: an ended control whose readings retention removed must
// not read like a device that never reported.
func TestDERControlList_ExpiredReadingsDifferFromNoReadings(t *testing.T) {
	d := newDeliveryHarness(t)
	d.h.MirrorReadingRetention = config.DefaultMirrorReadingRetention
	now := time.Now().Unix()
	old := now - 40*3600
	d.putMirror(t, "1", 300)
	w := int64(3600)
	for at := old; at < old+600; at += 300 {
		d.putReading(t, "1", time.Unix(at, 0).UnixNano(), "W", &w, exportWatts())
	}
	expired := d.seedControl(t, "x", old, 600, dercontrol.LifecycleRecord{})
	silent := d.seedControl(t, "s", now-3600, 600, dercontrol.LifecycleRecord{})
	ret := &mirrorretention.Retention{
		Readings:     d.mmrs,
		Mirrors:      d.mups,
		MaxAge:       config.DefaultMirrorReadingRetention,
		MaxPerSeries: config.DefaultMirrorReadingMaxPerSeries,
		Log:          slog.New(slog.DiscardHandler),
	}
	if removed, err := ret.Sweep(context.Background(), time.Now()); err != nil || removed != 2 {
		t.Fatalf("Sweep = %d, %v, want 2 removed", removed, err)
	}
	strip := func(mrid string) map[string]any {
		raw, err := json.Marshal(d.deliveryOf(t, mrid))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		delete(m, "windowStart")
		delete(m, "windowEnd")
		return m
	}
	e, s := strip(expired), strip(silent)
	if fmt.Sprint(e) == fmt.Sprint(s) {
		t.Fatalf("expired delivery %v reads exactly like no readings %v", e, s)
	}
	if e["readingsExpired"] != true || e["deliveredWh"] != nil || e["coveredSeconds"] != float64(0) {
		t.Fatalf("expired delivery = %v, want readingsExpired true and a null figure", e)
	}
	if s["readingsExpired"] != false {
		t.Fatalf("silent delivery = %v, want readingsExpired false", s)
	}
}

// Fix round item 4: an inline untyped reading takes its ReadingType from an
// out-of-band record of its mRID, so that record outlives its age while the
// inline reading relies on it.
func TestDERControlList_InlineReadingKeepsOutOfBandType(t *testing.T) {
	d := newDeliveryHarness(t)
	now := time.Now().Unix()
	w := int64(3600)
	d.putMirror(t, "1", 300, sep2.MirrorMeterReading{MRID: "W", LastUpdateTime: now - 600, Reading: &sep2.Reading{Value: &w}})
	d.putReading(t, "1", time.Unix(now-30*3600, 0).UnixNano(), "W", &w, exportWatts())
	mrid := d.seedControl(t, "e", now-600, 300, dercontrol.LifecycleRecord{})

	ret := &mirrorretention.Retention{
		Readings:     d.mmrs,
		Mirrors:      d.mups,
		MaxAge:       config.DefaultMirrorReadingRetention,
		MaxPerSeries: config.DefaultMirrorReadingMaxPerSeries,
		Log:          slog.New(slog.DiscardHandler),
	}
	if removed, err := ret.Sweep(context.Background(), time.Now()); err != nil || removed != 0 {
		t.Errorf("Sweep = %d, %v, want the typed record kept", removed, err)
	}
	if del := d.deliveryOf(t, mrid); del == nil || del.CoveredSeconds != 300 {
		t.Fatalf("delivery = %+v, want the inline reading's 300 s covered", del)
	}
}
