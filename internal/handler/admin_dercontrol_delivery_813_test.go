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

// #813 I-a across legs: separate component mirrors are flagged whatever leg
// each reports, and the figure still takes one path, never their sum. A sum
// of PV export 1000 W and storage import 3000 W over [b+300, b+600) would
// give -500 W.
func TestDeliveryMirrors_ComponentsOnDifferentLegsAreFlagged(t *testing.T) {
	b := deliveryBase
	pv := reading{mrid: "PV", at: b + 600, value: 1000, dir: reverseDir(), qual: dq(2), period: &sep2.DateTimeInterval{Start: b, Duration: 600}}
	for _, tc := range []struct {
		name    string
		storage []reading
	}{
		{"one storage reading", []reading{{mrid: "ST", at: b + 300, value: 3000, dir: forwardDir()}}},
		{"storage every 60 s", every("ST", true, b, 60, 10, reading{value: 3000, dir: forwardDir()})},
		{"storage on phase A", []reading{{mrid: "ST", at: b + 300, value: 3000, dir: forwardDir(), phase: phaseA}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeliveryHarness(t)
			d.seedReadings(t, "1", dcLFDI, roleIsDER, nil, pv)
			d.seedReadings(t, "2", dcLFDI, roleIsDER, nil, tc.storage...)
			mrid := d.seedControl(t, "e", b, 600, dercontrol.LifecycleRecord{})

			del := d.deliveryOf(t, mrid)
			if !del.ConcurrentMirrors {
				t.Fatalf("delivery = %+v, want concurrentMirrors", *del)
			}
			checkDelivery(t, d, mrid, 600, 1000)
		})
	}
}

// #813 I-a under 2023: Forward and Reverse from two concurrent mirrors are
// two components, so they are flagged and never netted; the newest reading
// holds, as for any overlap of concurrent mirrors. A net would give 4000 W.
func TestDelivery2023_ForwardReverseFromConcurrentMirrorsAreNotSummed(t *testing.T) {
	d := newDeliveryHarness(t)
	d.h.Edition = handler.Edition2023
	b := deliveryBase
	avg := dq(2)
	period := &sep2.DateTimeInterval{Start: b, Duration: 1000}
	d.seedReadings(t, "1", dcLFDI, roleIsDER, nil, reading{mrid: "R", at: b + 1000, value: 1000, dir: reverseDir(), qual: avg, period: period})
	d.seedReadings(t, "2", dcLFDI, roleIsDER, nil, reading{mrid: "F", at: b + 1001, value: 5000, dir: forwardDir(), qual: avg, period: period})
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if !del.ConcurrentMirrors || del.Readings != 1 || *del.NewestReadingTime != b+1001 {
		t.Fatalf("delivery = %+v, want concurrentMirrors and the newer Forward reading alone", *del)
	}
	checkDelivery(t, d, mrid, 1000, 5000)
}

// #813 I-a trap under 2023: a mirror opened after a restart is the same DER,
// so a second where the old mirror's Forward hold meets the new mirror's
// first Reverse is still netted, and nothing is flagged.
func TestDelivery2023_RestartedMirrorStillNets(t *testing.T) {
	d := newDeliveryHarness(t)
	d.h.Edition = handler.Edition2023
	b := deliveryBase
	f := reading{value: 5000, dir: forwardDir()}
	r := reading{value: 1000, dir: reverseDir()}
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300), append(every("F", true, b, 100, 5, f), every("R", true, b, 100, 5, r)...)...)
	// The new mirror posts Reverse 50 s before Forward, so [b+500, b+550)
	// takes the old mirror's Forward with the new mirror's Reverse.
	d.seedReadings(t, "2", dcLFDI, roleIsDER, ptrU32(300), append(every("R", true, b+500, 100, 5, r), every("F", true, b+550, 100, 5, f)...)...)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

	checkDelivery(t, d, mrid, 1000, 4000)
	if del := d.deliveryOf(t, mrid); del.ConcurrentMirrors {
		t.Fatalf("delivery = %+v, want a restarted mirror not flagged", *del)
	}
}

// #813 I-a: instantaneous mirrors posting in turn are concurrent even though
// each reading's hold ends at the other mirror's next reading. The figure
// takes each newest reading, never their sum (-2000 W).
func TestDeliveryMirrors_AlternatingMirrorsAreFlagged(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, nil, every("PV", true, b, 60, 10, reading{value: 1000, dir: reverseDir()})...)
	d.seedReadings(t, "2", dcLFDI, roleIsDER, nil, every("ST", true, b+30, 60, 10, reading{value: 3000, dir: forwardDir()})...)
	mrid := d.seedControl(t, "e", b, 600, dercontrol.LifecycleRecord{})

	if del := d.deliveryOf(t, mrid); !del.ConcurrentMirrors {
		t.Fatalf("delivery = %+v, want alternating mirrors flagged", *del)
	}
	checkDelivery(t, d, mrid, 600, -1000)
}

// #813 I-a: a reading whose sign cannot be mapped still shows its mirror
// reporting, so it counts toward concurrency while adding no watts.
func TestDeliveryMirrors_FlaggedReadingsCountTowardConcurrency(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	avg := dq(2)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, nil,
		reading{mrid: "PV", at: b + 600, value: 1000, qual: avg, period: &sep2.DateTimeInterval{Start: b, Duration: 600}})
	d.seedReadings(t, "2", dcLFDI, roleIsDER, nil,
		reading{mrid: "ST", at: b + 900, value: 3000, dir: reverseDir(), qual: avg, period: &sep2.DateTimeInterval{Start: b + 300, Duration: 600}})
	mrid := d.seedControl(t, "e", b, 900, dercontrol.LifecycleRecord{})

	del := d.deliveryOf(t, mrid)
	if !del.ConcurrentMirrors || !del.DirectionUnknown || del.Readings != 1 {
		t.Fatalf("delivery = %+v, want concurrentMirrors and directionUnknown beside the one mapped reading", *del)
	}
	checkDelivery(t, d, mrid, 600, 3000)
}

// #813 I-c: each rejected attribute of a DER W reading counts once, and a
// premises mirror's readings are never exclusions, as it is not DER output.
func TestDelivery_ExcludedReadingsByCause(t *testing.T) {
	b := deliveryBase
	for _, tc := range []struct {
		name     string
		roles    sep2.RoleFlagsValue
		r        reading
		excluded int
	}{
		{"maximum qualifier", roleIsDER, reading{qual: dq(8)}, 1},
		{"cumulative accumulation", roleIsDER, reading{acc: dq(4)}, 1},
		{"line-to-line phase AB", roleIsDER, reading{phase: dq(132)}, 1},
		{"line-to-line phase BC", roleIsDER, reading{phase: dq(66)}, 1},
		{"line-to-line phase CA", roleIsDER, reading{phase: dq(40)}, 1},
		{"premises mirror", roleIsPremA, reading{kind: dq(8)}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeliveryHarness(t)
			d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(100), every("P", true, b, 100, 10, reading{value: 1000, dir: reverseDir()})...)
			x := tc.r
			x.mrid, x.at, x.value, x.dir = "X", b+50, 9000, reverseDir()
			d.seedReadings(t, "2", dcLFDI, tc.roles, ptrU32(100), x)
			mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})

			del := d.deliveryOf(t, mrid)
			if del.ExcludedReadings != tc.excluded || del.Readings != 10 || del.ConcurrentMirrors {
				t.Fatalf("delivery = %+v, want %d excluded beside 10 used, unflagged", *del, tc.excluded)
			}
			checkDelivery(t, d, mrid, 1000, 1000)
		})
	}
}
