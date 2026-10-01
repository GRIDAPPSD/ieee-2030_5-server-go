package handler_test

import (
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

func forwardDir() *uint8 { v := sep2.FlowDirectionForward; return &v }

// #813 I-a 1: two DER mirrors covering one second of one leg are flagged,
// and the overlap still takes the newest reading, never the sum.
func TestDeliveryMirrors_ConcurrentMirrorsAreFlaggedNotSummed(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	avg := dq(2)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, nil,
		reading{mrid: "PV", at: b + 600, value: 1000, dir: reverseDir(), qual: avg, period: &sep2.DateTimeInterval{Start: b, Duration: 600}})
	d.seedReadings(t, "2", dcLFDI, roleIsDER, nil,
		reading{mrid: "ST", at: b + 900, value: 3000, dir: reverseDir(), qual: avg, period: &sep2.DateTimeInterval{Start: b + 300, Duration: 600}})
	mrid := d.seedControl(t, "e", b, 900, dercontrol.LifecycleRecord{})

	got, body := d.list(t)
	del := got[mrid].Delivery
	if del == nil || !del.ConcurrentMirrors || del.CoveredSeconds != 900 || del.Readings != 2 {
		t.Fatalf("delivery = %+v, want concurrentMirrors over 900 covered seconds: %s", del, body)
	}
	// [b, b+300) at 1000 W, then the newer 3000 W reading; a sum would add
	// 1000 W over [b+300, b+600).
	wantWh(t, del.DeliveredWh, (1000*300+3000*600)/3600.0)
	if !strings.Contains(body, `"concurrentMirrors":true`) {
		t.Fatalf("body lacks concurrentMirrors true: %s", body)
	}
}

// #813 I-a 2 and its trap: a mirror that starts after another stops is not
// concurrent, including a new mirror opened after a restart inside the old
// mirror's hold, as the EPRI client does.
func TestDeliveryMirrors_SequentialMirrorsAreNotConcurrent(t *testing.T) {
	b := deliveryBase
	for _, tc := range []struct {
		name   string
		second int64 // first receipt on the second mirror
	}{
		{"after the first stops", b + 600},
		{"restart inside the hold", b + 300},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeliveryHarness(t)
			d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300), every("A", true, b, 60, 4, reading{value: 1000, dir: reverseDir()})...)
			d.seedReadings(t, "2", dcLFDI, roleIsDER, ptrU32(300), every("B", true, tc.second, 60, 8, reading{value: 1000, dir: reverseDir()})...)
			mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

			del := d.deliveryOf(t, mrid)
			if del.ConcurrentMirrors {
				t.Fatalf("delivery = %+v, want sequential mirrors not flagged", *del)
			}
			if del.AverageW == nil || *del.AverageW != 1000 {
				t.Fatalf("delivery = %+v, want 1000 W", *del)
			}
		})
	}
}

// #813 I-b: under 2023 a leg's Forward and Reverse sum to Net, a second
// needs both once both are seen, and a Net reading ranks above the pair.
func TestDelivery2023_ForwardReverseNet(t *testing.T) {
	b := deliveryBase
	net := dq(4)
	for _, tc := range []struct {
		name     string
		rs       []reading
		covered  int64
		avgW     float64
		readings int
	}{
		{
			name:     "forward and reverse together",
			rs:       append(every("F", true, b, 100, 10, reading{value: 5000, dir: forwardDir()}), every("R", true, b, 100, 10, reading{value: 1000, dir: reverseDir()})...),
			covered:  1000,
			avgW:     4000,
			readings: 20,
		},
		{
			name:     "reverse stops halfway",
			rs:       append(every("F", true, b, 100, 10, reading{value: 5000, dir: forwardDir()}), every("R", true, b, 100, 5, reading{value: 1000, dir: reverseDir()})...),
			covered:  500,
			avgW:     4000,
			readings: 10,
		},
		{
			name: "net outranks the pair",
			rs: append(append(every("N", true, b, 100, 10, reading{value: 4000, dir: net}),
				every("F", true, b, 100, 10, reading{value: 6000, dir: forwardDir()})...),
				every("R", true, b, 100, 10, reading{value: 1000, dir: reverseDir()})...),
			covered:  1000,
			avgW:     4000,
			readings: 10,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeliveryHarness(t)
			d.h.Edition = handler.Edition2023
			d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(100), tc.rs...)
			mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})
			checkDelivery(t, d, mrid, tc.covered, tc.avgW)
			if del := d.deliveryOf(t, mrid); del.Readings != tc.readings || del.DirectionUnknown || del.ConcurrentMirrors {
				t.Fatalf("delivery = %+v, want %d readings, unflagged", *del, tc.readings)
			}
		})
	}
}

// #813 I-b: 2018 is unchanged, so Forward and Reverse stay one series and
// the newest reading holds, never a sum.
func TestDelivery2018_ForwardReverseStayOneSeries(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	rs := every("F", true, b, 100, 10, reading{value: 5000, dir: forwardDir()})
	rs = append(rs, every("R", true, b+1, 100, 10, reading{value: 1000, dir: reverseDir()})...)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(100), rs...)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})
	// Each 100 s: 1 s of Forward (import under 2018), then 99 s of Reverse.
	checkDelivery(t, d, mrid, 1000, (10*-5000+10*99*1000)/1000.0)
}

// #813 I-c: a DER W reading the figure cannot read is counted, the figure is
// unchanged, and non-W readings are not exclusions.
func TestDelivery_ExcludedReadingsAreCounted(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	kind8 := dq(8)
	hz := uint8(33) // UomType 33 is Hz
	rs := every("P", true, b, 100, 10, reading{value: 1000, dir: reverseDir()})
	rs = append(rs,
		reading{mrid: "K", at: b + 50, value: 9000, dir: reverseDir(), kind: kind8},
		reading{mrid: "Late", at: b + 5000, value: 9000, dir: reverseDir(), kind: kind8},
		reading{mrid: "Q", at: b + 60, value: 9000, uom: sep2.UomVars, dir: reverseDir(), kind: kind8},
		reading{mrid: "F", at: b + 70, value: 60, uom: hz, kind: kind8},
		reading{mrid: "V", at: b + 80, value: 240, uom: sep2.UomVolts, kind: kind8},
	)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(100), rs...)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	got, body := d.list(t)
	del := got[mrid].Delivery
	if del == nil || del.ExcludedReadings != 1 || del.Readings != 10 {
		t.Fatalf("delivery = %+v, want 1 excluded reading beside 10 used: %s", del, body)
	}
	checkDelivery(t, d, mrid, 1000, 1000)
	if !strings.Contains(body, `"excludedReadings":1`) {
		t.Fatalf("body lacks excludedReadings 1: %s", body)
	}
}
