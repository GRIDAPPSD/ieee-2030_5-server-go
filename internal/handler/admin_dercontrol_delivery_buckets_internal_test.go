package handler

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func evenBounds(from, to, step int64) []int64 {
	var b []int64
	for x := from; x <= to; x += step {
		b = append(b, x)
	}
	return b
}

// A device posting an instantaneous reading every 60 s holds each reading for
// 60 s, so every 5 s bucket inside the holds carries the held value and a
// bucket after the last hold ends has no covered second.
func TestIntegrateBuckets_HoldFillsBucketsAndGapsAfter(t *testing.T) {
	const base, we = int64(1000), int64(1300)
	post := uint32(60)
	uom, export := sep2.UomWatts, sep2.FlowDirectionReverse
	watts := []int64{400, 900}
	var rs []sep2.MirrorMeterReading
	for i, w := range watts {
		v := w
		rs = append(rs, sep2.MirrorMeterReading{
			MRID:           "W",
			LastUpdateTime: base + int64(i)*60,
			Reading:        &sep2.Reading{Value: &v},
			ReadingType:    &sep2.ReadingType{Uom: &uom, FlowDirection: &export},
		})
	}
	lr := powerLegs([]deviceMirror{{isDER: true, postRate: &post, readings: rs}}, Edition2018, base, we)
	sums, used := integrateBuckets(lr.spans, lr.seen, evenBounds(base, we, 5))
	if len(sums) != 60 {
		t.Fatalf("buckets = %d, want 60", len(sums))
	}
	if len(used) != 2 {
		t.Fatalf("used spans = %d, want 2", len(used))
	}
	for k, s := range sums {
		switch {
		case k < 12:
			if s.covered != 5 || s.wattSeconds != 5*400 {
				t.Errorf("bucket %d = %+v, want 5 s at 400 W", k, s)
			}
		case k < 24:
			if s.covered != 5 || s.wattSeconds != 5*900 {
				t.Errorf("bucket %d = %+v, want 5 s at 900 W", k, s)
			}
		default:
			if s.covered != 0 || s.wattSeconds != 0 {
				t.Errorf("bucket %d after the hold = %+v, want a gap", k, s)
			}
		}
	}
}

// Buckets over a window sum to the one-bucket integral of that window, and
// each bucket equals the one-bucket integral of its own stretch, on random
// spans with phases, qualifiers and directions mixed.
func TestIntegrateBuckets_SumsToOneBucket(t *testing.T) {
	legs := []leg{
		{average: true}, {}, {average: true, phase: phaseA}, {phase: phaseA}, {phase: phaseB}, {average: true, phase: phaseC},
		{dir: dirForward}, {dir: dirReverse}, {average: true, dir: dirForward}, {phase: phaseB, dir: dirReverse}, {phase: phaseB, dir: dirForward},
	}
	r := rand.New(rand.NewPCG(1112, 7))
	for c := range 300 {
		spans := map[leg][]powerSpan{}
		seen := map[leg]bool{}
		order := 0
		for _, l := range legs {
			if r.IntN(3) == 0 {
				continue
			}
			seen[l] = true
			for range r.IntN(5) {
				start := int64(r.IntN(200))
				spans[l] = append(spans[l], powerSpan{
					start: start, end: start + 1 + int64(r.IntN(80)),
					watts: float64(r.IntN(2000) - 500), received: int64(r.IntN(20)), order: order,
				})
				order++
			}
		}
		clone := func(lo, hi int64) map[leg][]powerSpan {
			out := map[leg][]powerSpan{}
			for l, ss := range spans {
				for _, s := range ss {
					s.start, s.end = max(s.start, lo), min(s.end, hi)
					if s.start < s.end {
						out[l] = append(out[l], s)
					}
				}
			}
			return out
		}
		step := int64(1 + r.IntN(40))
		bounds := evenBounds(0, 280, step)
		sums, _ := integrateBuckets(clone(-1<<40, 1<<40), seen, bounds)
		var gotWS float64
		var gotCov int64
		for k, s := range sums {
			ws, cov, _ := integrate(clone(bounds[k], bounds[k+1]), seen)
			if s.covered != cov || s.wattSeconds != ws {
				t.Fatalf("case %d bucket %d [%d,%d) = %+v, one-bucket over the stretch = (%v Ws, %d s)", c, k, bounds[k], bounds[k+1], s, ws, cov)
			}
			gotWS += s.wattSeconds
			gotCov += s.covered
		}
		wantWS, wantCov, _ := integrate(clone(bounds[0], bounds[len(bounds)-1]), seen)
		if gotCov != wantCov || gotWS != wantWS {
			t.Fatalf("case %d: buckets sum to (%v Ws, %d s), one bucket over the window gives (%v Ws, %d s)", c, gotWS, gotCov, wantWS, wantCov)
		}
	}
}

// Seconds outside the first and last bound belong to no bucket.
func TestIntegrateBuckets_DropsSecondsOutsideBounds(t *testing.T) {
	spans := map[leg][]powerSpan{{}: {{start: 0, end: 100, watts: 10, order: 0}}}
	seen := map[leg]bool{{}: true}
	sums, used := integrateBuckets(spans, seen, []int64{20, 30, 50})
	want := []bucketSum{{wattSeconds: 100, covered: 10}, {wattSeconds: 200, covered: 20}}
	if !slices.Equal(sums, want) {
		t.Fatalf("sums = %+v, want %+v", sums, want)
	}
	if len(used) != 1 {
		t.Fatalf("used = %d, want 1", len(used))
	}
}
