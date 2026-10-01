package handler

import (
	"slices"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// The mirror reading retention floor is built from this hold ceiling; a
// longer ceiling here without one there would let retention remove a reading
// the delivery figure still reaches (#806).
func TestMaxHoldSeconds_MatchesMirrorReadingRetentionFloor(t *testing.T) {
	if got := config.MirrorReadingMaxHold; got != maxHoldSeconds*time.Second {
		t.Fatalf("config.MirrorReadingMaxHold = %v, want %d s", got, maxHoldSeconds)
	}
}

// A window is expired once the earliest reading that can reach its start was
// received before the retention cutoff, including a window that ended after
// the cutoff but started too early to be whole.
func TestReadingsExpired_Boundary(t *testing.T) {
	const now, ret = int64(1_800_000_000), 90000 * time.Second
	cutoff := now - 90000
	for _, tc := range []struct {
		name string
		ws   int64
		ret  time.Duration
		want bool
	}{
		{"earliest reading at the cutoff", cutoff + maxHoldSeconds, ret, false},
		{"earliest reading one second past it", cutoff + maxHoldSeconds - 1, ret, true},
		{"ended after the cutoff, started before it", cutoff - 100, ret, true},
		{"retention unknown", cutoff - 100000, 0, false},
	} {
		if got := readingsExpired(tc.ws, now, tc.ret); got != tc.want {
			t.Errorf("%s: readingsExpired = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ReadingLegs keys by leg and direction, never by mRID: an untyped reading
// inherits its mRID's type, and a reading on no leg has the zero key.
func TestReadingLegs(t *testing.T) {
	w, v := sep2.UomWatts, sep2.UomVolts
	fwd, rev := sep2.FlowDirectionForward, sep2.FlowDirectionReverse
	val := int64(1)
	rd := func(mrid string, rt *sep2.ReadingType) sep2.MirrorMeterReading {
		return sep2.MirrorMeterReading{MRID: mrid, ReadingType: rt, Reading: &sep2.Reading{Value: &val}}
	}
	in := []sep2.MirrorMeterReading{
		rd("A", &sep2.ReadingType{Uom: &w, FlowDirection: &rev}),
		rd("B", &sep2.ReadingType{Uom: &w, FlowDirection: &rev}),
		rd("A", nil),
		rd("C", &sep2.ReadingType{Uom: &w, FlowDirection: &fwd}),
		rd("D", &sep2.ReadingType{Uom: &v}),
		rd("E", nil),
	}
	want := []ReadingLeg{
		{"instantaneous total", rev}, {"instantaneous total", rev}, {"instantaneous total", rev},
		{"instantaneous total", fwd}, {}, {},
	}
	got := ReadingLegs(in)
	if !slices.Equal(got, want) {
		t.Fatalf("ReadingLegs = %v, want %v", got, want)
	}
	if in[2].ReadingType != nil {
		t.Fatal("ReadingLegs filled the caller's reading")
	}
}
