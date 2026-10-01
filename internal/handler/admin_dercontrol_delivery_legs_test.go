package handler_test

import (
	"fmt"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// Phase codes (2018 PhaseCode): A 128, B 64.
var (
	phaseA = dq(128)
	phaseB = dq(64)
)

// every returns readings of one leg received every step seconds from start,
// n of them, each with a fresh mRID when fresh is set.
func every(prefix string, fresh bool, start, step int64, n int, r reading) []reading {
	out := make([]reading, n)
	for i := range out {
		out[i] = r
		out[i].at = start + int64(i)*step
		out[i].mrid = prefix
		if fresh {
			out[i].mrid = fmt.Sprintf("%s%d", prefix, i)
		}
	}
	return out
}

func checkDelivery(t *testing.T, d *deliveryHarness, mrid string, covered int64, avgW float64) {
	t.Helper()
	del := d.deliveryOf(t, mrid)
	if del.CoveredSeconds != covered || del.AverageW == nil {
		t.Fatalf("delivery = %+v, want %d covered seconds", *del, covered)
	}
	if got := *del.AverageW; got < avgW-1e-9 || got > avgW+1e-9 {
		t.Fatalf("averageW = %v, want %v (delivery %+v)", got, avgW, *del)
	}
}

// T1: an EPRI client gives every reading a fresh mRID; they are one leg, so
// each holds until the next.
func TestDeliveryLegs_T1FreshMRIDsAreOneLeg(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, nil, every("M", true, b, 60, 20, reading{value: 1000, dir: reverseDir()})...)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})
	checkDelivery(t, d, mrid, 1000, 1000)
}

// T2: a total beside its phases takes the total; the phases are not added.
func TestDeliveryLegs_T2TotalOutranksPhases(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900),
		reading{mrid: "T", at: b, value: 2000, dir: reverseDir()},
		reading{mrid: "A", at: b, value: 1000, dir: reverseDir(), phase: phaseA},
		reading{mrid: "B", at: b, value: 1000, dir: reverseDir(), phase: phaseB},
	)
	mrid := d.seedControl(t, "e", b, 900, dercontrol.LifecycleRecord{})
	checkDelivery(t, d, mrid, 900, 2000)
	if del := d.deliveryOf(t, mrid); del.Readings != 1 {
		t.Fatalf("readings = %d, want 1 (the total)", del.Readings)
	}
}

// T3: an instantaneous and an Average series of one output take the Average.
func TestDeliveryLegs_T3AverageOutranksInstantaneous(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900),
		reading{mrid: "I", at: b, value: 1000, dir: reverseDir()},
		reading{mrid: "V", at: b + 900, value: 1000, dir: reverseDir(), qual: dq(2), period: &sep2.DateTimeInterval{Start: b, Duration: 900}},
	)
	mrid := d.seedControl(t, "e", b, 900, dercontrol.LifecycleRecord{})
	checkDelivery(t, d, mrid, 900, 1000)
	del := d.deliveryOf(t, mrid)
	if del.Readings != 1 || *del.NewestReadingTime != b+900 {
		t.Fatalf("delivery = %+v, want the Average reading only", *del)
	}
}

// T4: a series renamed halfway is still one leg, so every second is covered.
func TestDeliveryLegs_T4RenamedSeries(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	rs := append(every("X", false, b, 100, 5, reading{value: 1000, dir: reverseDir()}),
		every("Y", false, b+500, 100, 5, reading{value: 1000, dir: reverseDir()})...)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300), rs...)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})
	checkDelivery(t, d, mrid, 1000, 1000)
}

// T5: a second DER mirror with one early reading joins the leg; the two are
// never added.
func TestDeliveryLegs_T5SecondMirrorJoinsLeg(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300), every("P", false, b, 60, 17, reading{value: 1000, dir: reverseDir()})...)
	d.seedReadings(t, "2", dcLFDI, roleIsDER, ptrU32(300), reading{mrid: "Q", at: b, value: 1000, dir: reverseDir()})
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})
	checkDelivery(t, d, mrid, 1000, 1000)
}

// T6: a total that stops halfway hands over to the phase sum.
func TestDeliveryLegs_T6TotalThenPhases(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	rs := every("T", false, b, 100, 5, reading{value: 1200, dir: reverseDir()})
	rs = append(rs, every("A", false, b, 100, 10, reading{value: 400, dir: reverseDir(), phase: phaseA})...)
	rs = append(rs, every("B", false, b, 100, 10, reading{value: 600, dir: reverseDir(), phase: phaseB})...)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(100), rs...)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})
	// [b, b+500) at 1200 W from the total, [b+500, b+1000) at 1000 W from A+B.
	checkDelivery(t, d, mrid, 1000, 1100)
	if del := d.deliveryOf(t, mrid); del.Readings != 15 {
		t.Fatalf("readings = %d, want 5 total + 5 A + 5 B", del.Readings)
	}
}

// T7: a W series of another kind is not delivery.
func TestDeliveryLegs_T7OtherKindIgnored(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900),
		reading{mrid: "T", at: b, value: 1000, dir: reverseDir(), kind: dq(37), acc: dq(12)},
		reading{mrid: "K", at: b, value: 5000, dir: reverseDir(), kind: dq(8)},
		reading{mrid: "C", at: b, value: 5000, dir: reverseDir(), acc: dq(4)},
	)
	mrid := d.seedControl(t, "e", b, 900, dercontrol.LifecycleRecord{})
	checkDelivery(t, d, mrid, 900, 1000)
}

// T8 (pin): phases with no total, B stopping halfway, cover only the first
// half; B is never read as 0 W.
func TestDeliveryLegs_T8DroppedPhaseUncovers(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	rs := every("A", false, b, 100, 10, reading{value: 400, dir: reverseDir(), phase: phaseA})
	rs = append(rs, every("B", false, b, 100, 5, reading{value: 600, dir: reverseDir(), phase: phaseB})...)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(100), rs...)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})
	checkDelivery(t, d, mrid, 500, 1000)
}

// T9 (pin): under 2018 a DER's Forward -500 is -500 W and 0 W under
// direction 0 is 0 W, both unflagged.
func TestDeliveryLegs_T9SignPins(t *testing.T) {
	fwd, none := sep2.FlowDirectionForward, uint8(0)
	for _, tc := range []struct {
		name  string
		r     reading
		wantW float64
	}{
		{"forward -500", reading{mrid: "P", value: -500, dir: &fwd}, -500},
		{"zero, direction 0", reading{mrid: "P", value: 0, dir: &none}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeliveryHarness(t)
			tc.r.at = deliveryBase
			d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(900), tc.r)
			mrid := d.seedControl(t, "e", deliveryBase, 900, dercontrol.LifecycleRecord{})
			checkDelivery(t, d, mrid, 900, tc.wantW)
			if d.deliveryOf(t, mrid).DirectionUnknown {
				t.Fatal("flagged, want unflagged")
			}
		})
	}
}

// T10 (pin): staggered phases cover exactly the seconds both cover; one
// phase's reading never cuts another phase's hold.
func TestDeliveryLegs_T10StaggeredPhases(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	rs := every("A", false, b, 300, 3, reading{value: 400, dir: reverseDir(), phase: phaseA})
	rs = append(rs, every("B", false, b+100, 300, 3, reading{value: 600, dir: reverseDir(), phase: phaseB})...)
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300), rs...)
	mrid := d.seedControl(t, "e", b, 1000, dercontrol.LifecycleRecord{})
	// A covers [b, b+900), B [b+100, b+1000): both over [b+100, b+900).
	checkDelivery(t, d, mrid, 800, 1000)
}

// Absorbed coverage LOW: two readings of one leg received in the same second,
// the first without a direction, flag the figure; the directed one covers.
func TestDeliveryLegs_EqualReceiptStillFlags(t *testing.T) {
	d := newDeliveryHarness(t)
	b := deliveryBase
	d.seedReadings(t, "1", dcLFDI, roleIsDER, ptrU32(300),
		reading{mrid: "P0", at: b, value: 9999},
		reading{mrid: "P1", at: b, value: 1000, dir: reverseDir()},
	)
	mrid := d.seedControl(t, "e", b, 300, dercontrol.LifecycleRecord{})
	checkDelivery(t, d, mrid, 300, 1000)
	if !d.deliveryOf(t, mrid).DirectionUnknown {
		t.Fatal("not flagged, want the directionless reading flagged")
	}
}
