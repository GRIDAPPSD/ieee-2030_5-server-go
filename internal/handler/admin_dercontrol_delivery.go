package handler

import (
	"cmp"
	"container/heap"
	"slices"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// DERControlDelivery is the metered energy a control's device exported over
// the control's effective window (#802), from its real-power mirror readings.
// Times are Unix seconds. DeliveredWh is export-positive and null when no
// reading covers any second of the window; CoveredSeconds says how much of
// [WindowStart, WindowEnd) the figure stands on, and the rest is uncovered,
// never filled. Readings counts the readings the figure uses, and
// NewestReadingTime is the newest of them, by server receipt time.
type DERControlDelivery struct {
	WindowStart       int64    `json:"windowStart"`
	WindowEnd         int64    `json:"windowEnd"`
	DeliveredWh       *float64 `json:"deliveredWh"`
	CoveredSeconds    int64    `json:"coveredSeconds"`
	Readings          int      `json:"readings"`
	DirectionUnknown  bool     `json:"directionUnknown"`
	DeviceLFDI        string   `json:"deviceLFDI"`
	NewestReadingTime *int64   `json:"newestReadingTime"`
}

// dataQualifierAverage is DataQualifierType 2, "Average" (CSIP Table 3).
const dataQualifierAverage uint8 = 2

// defaultHoldSeconds bounds the hold of a reading with no period when its
// mirror carries no postRate.
const defaultHoldSeconds = 300

// effectiveWindow is the part of a control's interval it was in force: from
// its start to the earliest of its end, its supersede time, its cancel time
// and now. It never ends before it starts.
func effectiveWindow(interval sep2.DateTimeInterval, lc dercontrol.LifecycleRecord, now int64) (start, end int64) {
	start = interval.Start
	end = interval.Start + int64(interval.Duration)
	if lc.SupersededAt != nil {
		end = min(end, *lc.SupersededAt)
	}
	if lc.CancelledAt != nil {
		end = min(end, *lc.CancelledAt)
	}
	end = min(end, now)
	return start, max(start, end)
}

// powerSpan is one real-power reading's export-positive watts over [start, end).
type powerSpan struct {
	start, end int64
	watts      float64
	received   int64 // server receipt time, the tie-break between overlapping spans
	order      int
}

// powerSpans turns a device's real-power readings into the spans they cover.
//
// An Average reading covers its timePeriod. Its other fallback, the
// ReadingType's intervalLength, is not decoded by the vendored core, so an
// Average reading without a timePeriod falls to the hold rule below.
//
// Hold rule, INFERRED (#802), since the standard sets none: a reading with no
// period holds its value from its receipt time until the next reading of the
// same mirror and mRID, for at most the mirror's postRate, or
// defaultHoldSeconds without one.
//
// A reading whose direction cannot be mapped yields no span and sets
// undirected when it would have covered any part of [ws, we).
func powerSpans(mirrors []deviceMirror, edition SEP2Edition, ws, we int64) (spans []powerSpan, undirected bool) {
	for _, m := range mirrors {
		hold := int64(defaultHoldSeconds)
		if m.postRate != nil && *m.postRate > 0 {
			hold = int64(*m.postRate)
		}
		power := make([]sep2.MirrorMeterReading, 0, len(m.readings))
		for _, r := range m.readings {
			rt := r.ReadingType
			if rt == nil || rt.Uom == nil || *rt.Uom != sep2.UomWatts || r.Reading == nil || r.Reading.Value == nil {
				continue
			}
			power = append(power, r)
		}
		slices.SortStableFunc(power, func(a, b sep2.MirrorMeterReading) int {
			return cmp.Or(cmp.Compare(a.MRID, b.MRID), cmp.Compare(a.LastUpdateTime, b.LastUpdateTime))
		})
		for i, r := range power {
			start, end := r.LastUpdateTime, r.LastUpdateTime+hold
			if tp := r.Reading.TimePeriod; tp != nil && tp.Duration > 0 && isAverage(r.ReadingType) {
				start, end = tp.Start, tp.Start+int64(tp.Duration)
			} else {
				for _, next := range power[i+1:] {
					if next.MRID != r.MRID {
						break
					}
					if next.LastUpdateTime > r.LastUpdateTime {
						end = min(end, next.LastUpdateTime)
						break
					}
				}
			}
			start, end = max(start, ws), min(end, we)
			if start >= end {
				continue
			}
			multiplier := int8(0)
			if r.ReadingType.PowerOfTenMultiplier != nil {
				multiplier = *r.ReadingType.PowerOfTenMultiplier
			}
			watts, mapped := exportPositive(scaledValue(float64(*r.Reading.Value), multiplier), r.ReadingType.FlowDirection, edition, m.isDER)
			if !mapped {
				undirected = true
				continue
			}
			spans = append(spans, powerSpan{start: start, end: end, watts: watts, received: r.LastUpdateTime, order: len(spans)})
		}
	}
	return spans, undirected
}

func isAverage(rt *sep2.ReadingType) bool {
	return rt.DataQualifier != nil && *rt.DataQualifier == dataQualifierAverage
}

// integrate sums watt-seconds over the union of spans, counting each second
// once: where spans overlap, the most recently received one is used.
func integrate(spans []powerSpan) (wattSeconds float64, covered int64, used []powerSpan) {
	if len(spans) == 0 {
		return 0, 0, nil
	}
	slices.SortFunc(spans, func(a, b powerSpan) int { return cmp.Compare(a.start, b.start) })
	bounds := make([]int64, 0, 2*len(spans))
	for _, s := range spans {
		bounds = append(bounds, s.start, s.end)
	}
	slices.Sort(bounds)
	bounds = slices.Compact(bounds)

	usedOrder := map[int]bool{}
	active := &spanHeap{}
	next := 0
	for i := 0; i+1 < len(bounds); i++ {
		x := bounds[i]
		for next < len(spans) && spans[next].start == x {
			heap.Push(active, spans[next])
			next++
		}
		for active.Len() > 0 && (*active)[0].end <= x {
			heap.Pop(active)
		}
		if active.Len() == 0 {
			continue
		}
		top := (*active)[0]
		seconds := bounds[i+1] - x
		wattSeconds += top.watts * float64(seconds)
		covered += seconds
		if !usedOrder[top.order] {
			usedOrder[top.order] = true
			used = append(used, top)
		}
	}
	return wattSeconds, covered, used
}

// spanHeap keeps the most recently received span on top.
type spanHeap []powerSpan

func (h spanHeap) Len() int { return len(h) }
func (h spanHeap) Less(i, j int) bool {
	if h[i].received != h[j].received {
		return h[i].received > h[j].received
	}
	return h[i].order > h[j].order
}
func (h spanHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *spanHeap) Push(x any)   { *h = append(*h, x.(powerSpan)) }
func (h *spanHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// newDelivery computes one control's delivery from its device's mirrors.
func newDelivery(deviceLFDI string, mirrors []deviceMirror, edition SEP2Edition, ws, we int64) *DERControlDelivery {
	d := &DERControlDelivery{WindowStart: ws, WindowEnd: we, DeviceLFDI: deviceLFDI}
	spans, undirected := powerSpans(mirrors, edition, ws, we)
	d.DirectionUnknown = undirected
	wattSeconds, covered, used := integrate(spans)
	if covered == 0 {
		return d
	}
	wh := wattSeconds / 3600
	d.DeliveredWh = &wh
	d.CoveredSeconds = covered
	d.Readings = len(used)
	newest := used[0].received
	for _, s := range used[1:] {
		newest = max(newest, s.received)
	}
	d.NewestReadingTime = &newest
	return d
}
