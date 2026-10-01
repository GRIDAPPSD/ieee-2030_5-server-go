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
// never filled. AverageW is DeliveredWh over the covered seconds, in watts,
// the figure comparable with a target power. Readings counts the readings the
// figure uses, and NewestReadingTime is the newest of them, by server
// receipt time.
type DERControlDelivery struct {
	WindowStart       int64    `json:"windowStart"`
	WindowEnd         int64    `json:"windowEnd"`
	DeliveredWh       *float64 `json:"deliveredWh"`
	AverageW          *float64 `json:"averageW"`
	CoveredSeconds    int64    `json:"coveredSeconds"`
	Readings          int      `json:"readings"`
	DirectionUnknown  bool     `json:"directionUnknown"`
	DeviceLFDI        string   `json:"deviceLFDI"`
	NewestReadingTime *int64   `json:"newestReadingTime"`
}

// DataQualifierType values. 0 is "Not applicable (default, if not
// specified)" (2018 DataQualifierType), so it reads as an absent qualifier.
// 2 is "Average over the Interval (the last posting)" (CSIP Table 3).
const (
	dataQualifierNone    uint8 = 0
	dataQualifierAverage uint8 = 2
)

// Hold bounds, INFERRED (#802): the standard sets no hold. 300 s is this
// server's own default for a mirror with no postRate; 900 s is the postRate
// 2023 assumes when none is given, used here as a ceiling so a device's
// declared rate cannot stretch one reading across a whole window.
const (
	defaultHoldSeconds = 300
	maxHoldSeconds     = 900
)

// holdSeconds is how far one reading with no period may reach. A postRate of
// 0 is treated as absent.
func holdSeconds(postRate *uint32) int64 {
	if postRate == nil || *postRate == 0 {
		return defaultHoldSeconds
	}
	return min(int64(*postRate), maxHoldSeconds)
}

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

// countsAsDelivery reports whether a reading is DER real power that may be
// integrated: uom W, instantaneous (no qualifier, or 0) or Average. Maximum
// and Minimum series describe extremes, not energy.
func countsAsDelivery(r sep2.MirrorMeterReading) bool {
	rt := r.ReadingType
	if rt == nil || rt.Uom == nil || *rt.Uom != sep2.UomWatts || r.Reading == nil || r.Reading.Value == nil {
		return false
	}
	q := rt.DataQualifier
	return q == nil || *q == dataQualifierNone || *q == dataQualifierAverage
}

// powerSeries turns a device's DER real-power readings into spans, one slice
// per series (a mirror and mRID; per-phase series are distinct mRIDs).
//
// An Average reading covers its timePeriod. Without one (intervalLength is
// not decoded by the vendored core) it covers the hold before its receipt,
// back to no earlier than the previous reading of its series. An
// instantaneous reading holds forward from its receipt until the next reading
// of its series, for at most the hold. See holdSeconds.
//
// Only isDER mirrors count: a premises mirror measures net site power, not
// the DER output a control targets. A reading whose sign cannot be mapped
// (exportPositive) yields no span and sets flagged when it would have covered
// any part of [ws, we).
func powerSeries(mirrors []deviceMirror, edition SEP2Edition, ws, we int64) (series [][]powerSpan, flagged bool) {
	order := 0
	for _, m := range mirrors {
		if !m.isDER {
			continue
		}
		hold := holdSeconds(m.postRate)
		power := make([]sep2.MirrorMeterReading, 0, len(m.readings))
		for _, r := range m.readings {
			if countsAsDelivery(r) {
				power = append(power, r)
			}
		}
		slices.SortStableFunc(power, func(a, b sep2.MirrorMeterReading) int {
			return cmp.Or(cmp.Compare(a.MRID, b.MRID), cmp.Compare(a.LastUpdateTime, b.LastUpdateTime))
		})
		var spans []powerSpan
		for i, r := range power {
			if i > 0 && power[i-1].MRID != r.MRID {
				series = append(series, spans)
				spans = nil
			}
			start, end := readingCoverage(power, i, hold)
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
				flagged = true
				continue
			}
			spans = append(spans, powerSpan{start: start, end: end, watts: watts, received: r.LastUpdateTime, order: order})
			order++
		}
		series = append(series, spans)
	}
	return series, flagged
}

// readingCoverage is the span power[i] covers before window clipping. power
// is sorted by mRID, then receipt time.
func readingCoverage(power []sep2.MirrorMeterReading, i int, hold int64) (start, end int64) {
	r := power[i]
	at := r.LastUpdateTime
	if !isAverage(r.ReadingType) {
		end = at + hold
		for _, next := range power[i+1:] {
			if next.MRID != r.MRID {
				break
			}
			if next.LastUpdateTime > at {
				end = min(end, next.LastUpdateTime)
				break
			}
		}
		return at, end
	}
	if tp := r.Reading.TimePeriod; tp != nil && tp.Duration > 0 {
		return tp.Start, tp.Start + int64(tp.Duration)
	}
	start = at - hold
	for j := i - 1; j >= 0 && power[j].MRID == r.MRID; j-- {
		if power[j].LastUpdateTime < at {
			start = max(start, power[j].LastUpdateTime)
			break
		}
	}
	return start, at
}

func isAverage(rt *sep2.ReadingType) bool {
	return rt.DataQualifier != nil && *rt.DataQualifier == dataQualifierAverage
}

// piece is a stretch of one series' power, after its overlaps are resolved.
type piece struct {
	start, end int64
	span       powerSpan
}

// resolveSeries splits one series' spans into disjoint pieces: where its own
// spans overlap, the most recently received one is used.
func resolveSeries(spans []powerSpan) []piece {
	if len(spans) == 0 {
		return nil
	}
	slices.SortFunc(spans, func(a, b powerSpan) int { return cmp.Compare(a.start, b.start) })
	bounds := make([]int64, 0, 2*len(spans))
	for _, s := range spans {
		bounds = append(bounds, s.start, s.end)
	}
	slices.Sort(bounds)
	bounds = slices.Compact(bounds)

	var pieces []piece
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
		if active.Len() > 0 {
			pieces = append(pieces, piece{start: x, end: bounds[i+1], span: (*active)[0]})
		}
	}
	return pieces
}

// integrate sums watt-seconds across series, since parallel series (phases,
// several DER mirrors) are parts of one output. A second is covered only when
// every series with a span in the window covers it: a second some series
// miss would understate the output, so it is left uncovered and its energy
// is not counted.
func integrate(series [][]powerSpan) (wattSeconds float64, covered int64, used []powerSpan) {
	var all []piece
	reporting := 0
	for _, spans := range series {
		pieces := resolveSeries(spans)
		if len(pieces) > 0 {
			reporting++
		}
		all = append(all, pieces...)
	}
	if reporting == 0 {
		return 0, 0, nil
	}
	type edge struct {
		at    int64
		piece int
		open  bool
	}
	edges := make([]edge, 0, 2*len(all))
	for i, p := range all {
		edges = append(edges, edge{p.start, i, true}, edge{p.end, i, false})
	}
	slices.SortFunc(edges, func(a, b edge) int { return cmp.Compare(a.at, b.at) })

	// Pieces of one series are disjoint, so the active pieces belong to
	// distinct series.
	active := map[int]powerSpan{}
	usedOrder := map[int]bool{}
	for i := 0; i < len(edges); {
		x := edges[i].at
		for ; i < len(edges) && edges[i].at == x; i++ {
			if edges[i].open {
				active[edges[i].piece] = all[edges[i].piece].span
			} else {
				delete(active, edges[i].piece)
			}
		}
		if i == len(edges) || len(active) < reporting {
			continue
		}
		seconds := edges[i].at - x
		covered += seconds
		for _, sp := range active {
			wattSeconds += sp.watts * float64(seconds)
			if !usedOrder[sp.order] {
				usedOrder[sp.order] = true
				used = append(used, sp)
			}
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
	series, flagged := powerSeries(mirrors, edition, ws, we)
	d.DirectionUnknown = flagged
	wattSeconds, covered, used := integrate(series)
	if covered == 0 {
		return d
	}
	wh := wattSeconds / 3600
	avg := wattSeconds / float64(covered)
	d.DeliveredWh = &wh
	d.AverageW = &avg
	d.CoveredSeconds = covered
	d.Readings = len(used)
	newest := used[0].received
	for _, s := range used[1:] {
		newest = max(newest, s.received)
	}
	d.NewestReadingTime = &newest
	return d
}
