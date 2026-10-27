package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

type latestAnswer struct {
	LatestW       *float64
	LatestAt      *int64
	LatestMissing []string
	Devices       []PowerDevice
}

func (p *powerHarness) latest(query string) latestAnswer {
	p.t.Helper()
	code, body := p.get(query)
	if code != 200 {
		p.t.Fatalf("status = %d, body %s", code, body)
	}
	var a latestAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		p.t.Fatal(err)
	}
	return a
}

func (a latestAnswer) device(lfdi string) *PowerDevice {
	for i := range a.Devices {
		if a.Devices[i].LFDI == lfdi {
			return &a.Devices[i]
		}
	}
	return nil
}

// postAverage stores a mirror with one Average W reading received at at.
func (p *powerHarness) postAverage(deviceLFDI string, postRate uint32, at, watts int64) {
	p.t.Helper()
	p.n++
	id := fmt.Sprint(p.n)
	mup := sep2.MirrorUsagePoint{MRID: "MUP" + id, DeviceLFDI: deviceLFDI, PostRate: &postRate, RoleFlags: roleFlagIsDER}
	mup.Href = "/mup/" + id
	if err := p.mups.Create(context.Background(), id, mup); err != nil {
		p.t.Fatal(err)
	}
	uom, dq, dir := sep2.UomWatts, dataQualifierAverage, sep2.FlowDirectionReverse
	r := sep2.MirrorMeterReading{
		MRID:           "W" + id,
		LastUpdateTime: at,
		Reading:        &sep2.Reading{Value: &watts},
		ReadingType:    &sep2.ReadingType{Uom: &uom, DataQualifier: &dq, FlowDirection: &dir},
	}
	if err := p.mmrs.Create(context.Background(), id, "0", r); err != nil {
		p.t.Fatal(err)
	}
}

// Two devices posting 3 kW and -1 kW give a total of 2000 W, each device
// keeps its own value, and the age is the oldest bucket end used.
func TestPower_LatestWSumsDevicesNewestBucket(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 3000})
	p.post(powerD2, roleFlagIsDER, 50, sep2.FlowDirectionForward, [2]int64{powerNow - 80, 1000})

	a := p.latest("aggregatorLFDI=" + powerAgg + "&window=3600")
	if a.LatestW == nil || *a.LatestW != 2000 {
		t.Fatalf("latestW = %v, want 2000", a.LatestW)
	}
	if a.LatestAt == nil || *a.LatestAt != powerNow-50 {
		t.Fatalf("latestAt = %v, want %d", a.LatestAt, powerNow-50)
	}
	if len(a.LatestMissing) != 0 {
		t.Fatalf("latestMissing = %v, want none", a.LatestMissing)
	}
	d1, d2 := a.device(powerD1), a.device(powerD2)
	if d1 == nil || d1.LatestW == nil || *d1.LatestW != 3000 || *d1.LatestAt != powerNow-50 {
		t.Fatalf("device 1 = %+v, want 3000 W at %d", d1, powerNow-50)
	}
	if d2 == nil || d2.LatestW == nil || *d2.LatestW != -1000 || *d2.LatestAt != powerNow-30 {
		t.Fatalf("device 2 = %+v, want -1000 W at %d", d2, powerNow-30)
	}
}

// A device posting only 300 s Averages has a value 200 s after its last post,
// where the chart's newest bucket has none for it.
func TestPower_LatestWAverageOnlyDeviceHasValueBetweenPosts(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1)
	p.postAverage(powerD1, 300, powerNow-200, 4000)

	a := p.latest("aggregatorLFDI=" + powerAgg + "&window=900")
	if a.LatestW == nil || *a.LatestW != 4000 {
		t.Fatalf("latestW = %v, want 4000", a.LatestW)
	}
	if a.LatestAt == nil || *a.LatestAt != powerNow-200 {
		t.Fatalf("latestAt = %v, want %d", a.LatestAt, powerNow-200)
	}
}

// A device that reported in the chart window but not in the last 900 s is
// listed, and the total is null rather than the other device's value.
func TestPower_LatestWNullAndListsDeviceMissingFromLastSpan(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 3000})
	p.post(powerD2, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 2000, 1000})

	a := p.latest("aggregatorLFDI=" + powerAgg + "&window=3600")
	if a.LatestW != nil || a.LatestAt != nil {
		t.Fatalf("latestW = %v latestAt = %v, want both null", a.LatestW, a.LatestAt)
	}
	if len(a.LatestMissing) != 1 || a.LatestMissing[0] != powerD2 {
		t.Fatalf("latestMissing = %v, want [%s]", a.LatestMissing, powerD2)
	}
	d1, d2 := a.device(powerD1), a.device(powerD2)
	if d1 == nil || d1.LatestW == nil || *d1.LatestW != 3000 {
		t.Fatalf("device 1 = %+v, want its own 3000 W kept", d1)
	}
	if d2 == nil || d2.LatestW != nil || d2.LatestAt != nil {
		t.Fatalf("device 2 = %+v, want null latestW and latestAt", d2)
	}
}

// The value does not depend on the chart window, and an empty fleet has a
// null total and an empty, not null, missing list.
func TestPower_LatestWIgnoresWindowAndEmptyShape(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 3000})
	for _, w := range []string{"900", "3600", "86400"} {
		a := p.latest("aggregatorLFDI=" + powerAgg + "&window=" + w)
		if a.LatestW == nil || *a.LatestW != 3000 {
			t.Fatalf("window %s: latestW = %v, want 3000", w, a.LatestW)
		}
	}

	e := newPowerHarness(t)
	code, body := e.get("scope=all&window=900")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["latestW"]) != "null" || string(raw["latestMissing"]) != "[]" {
		t.Fatalf("latestW = %s latestMissing = %s, want null and []", raw["latestW"], raw["latestMissing"])
	}
}

// latestGrid ends on the last closed 5 s boundary and spans 900 s.
func TestLatestGrid(t *testing.T) {
	b := latestGrid(powerNow + 3)
	if len(b) != 181 || b[0] != powerNow-900 || b[len(b)-1] != powerNow {
		t.Fatalf("grid = %d bounds from %d to %d, want 181 from %d to %d", len(b), b[0], b[len(b)-1], powerNow-900, powerNow)
	}
}

// The live kW and its missing list do not depend on the chart window: a
// device last heard 2000 s ago is missing at every window, and the total is
// null at every window.
func TestPower_LatestWAndMissingSameAtEveryWindow(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 3000})
	p.post(powerD2, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 2000, 2000})
	for _, w := range []string{"900", "3600", "86400"} {
		a := p.latest("aggregatorLFDI=" + powerAgg + "&window=" + w)
		if a.LatestW != nil {
			t.Fatalf("window %s: latestW = %v, want null", w, *a.LatestW)
		}
		if len(a.LatestMissing) != 1 || a.LatestMissing[0] != powerD2 {
			t.Fatalf("window %s: latestMissing = %v, want [%s]", w, a.LatestMissing, powerD2)
		}
	}
}

// A device last heard beyond the reported span is not missing: it reported
// in none of the spans that matter, so the total is the other device's.
func TestPower_LatestWIgnoresDeviceSilentBeyondReportedSpan(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 3000})
	p.post(powerD2, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 5000, 2000})
	for _, w := range []string{"900", "86400"} {
		a := p.latest("aggregatorLFDI=" + powerAgg + "&window=" + w)
		if a.LatestW == nil || *a.LatestW != 3000 || len(a.LatestMissing) != 0 {
			t.Fatalf("window %s: latestW = %v missing = %v, want 3000 and none", w, a.LatestW, a.LatestMissing)
		}
	}
}

// A newest bucket covered for 3 of its 5 s still reads the device's average
// over the covered seconds, not the energy spread over the whole bucket.
func TestPower_LatestWPartlyCoveredBucketIsAveragedOverCoveredSeconds(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1)
	p.post(powerD1, roleFlagIsDER, 3, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 3000})
	a := p.latest("aggregatorLFDI=" + powerAgg + "&window=900")
	if a.LatestW == nil || *a.LatestW != 3000 {
		t.Fatalf("latestW = %v, want 3000", a.LatestW)
	}
}
