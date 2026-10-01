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
//
// ConcurrentMirrors is set when two DER mirrors of the device cover one
// second of one leg: a combined DER is one mirror (2023 10.10.4.4.1), so the
// figure may undercount separate components and they are never summed.
// ExcludedReadings counts DER real-power readings reaching the window that
// the figure cannot read (an unhandled kind, qualifier, accumulation or
// phase).
type DERControlDelivery struct {
	WindowStart       int64    `json:"windowStart"`
	WindowEnd         int64    `json:"windowEnd"`
	DeliveredWh       *float64 `json:"deliveredWh"`
	AverageW          *float64 `json:"averageW"`
	CoveredSeconds    int64    `json:"coveredSeconds"`
	Readings          int      `json:"readings"`
	DirectionUnknown  bool     `json:"directionUnknown"`
	ConcurrentMirrors bool     `json:"concurrentMirrors"`
	ExcludedReadings  int      `json:"excludedReadings"`
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

// KindType 37 is power; AccumulationBehaviourType 6 is indicating and 12 is
// instantaneous (2018 Table E.2 gives DER active power kind 37 and
// accumulation 12).
const (
	kindPower                 uint8 = 37
	accumulationIndicating    uint8 = 6
	accumulationInstantaneous uint8 = 12
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

// leg is one quantity a device reports, whatever mRID or mirror carries it:
// its qualifier (Average or instantaneous) and its phase (0 is the total).
// The EPRI client posts every reading under a fresh mRID, and a client may
// open a new mirror after a restart, so neither may split a leg. Under 2023
// only, dir splits a leg's Forward and Reverse readings into two sub-series
// whose sum is Net (see legValue); every other direction stays in dirMain.
type leg struct {
	average bool
	phase   uint8
	dir     uint8
}

const (
	dirMain uint8 = iota
	dirForward
	dirReverse
)

// Phase legs (2018 PhaseCode): the total is absent, 0 or ABC (224); A is
// 128 or AN 129, B 64 or BN 65, C 32 or CN 33. Line-to-line codes are not
// read.
const (
	phaseTotal uint8 = iota
	phaseA
	phaseB
	phaseC
)

// legOf reports the leg a reading belongs to, or false when it is not DER
// real power that may be integrated: uom W; qualifier absent, 0 or Average
// (Maximum and Minimum are extremes, not energy); kind absent, 0 or power;
// accumulation absent, 0, indicating or instantaneous.
func legOf(r sep2.MirrorMeterReading) (leg, bool) {
	rt := r.ReadingType
	if rt == nil || rt.Uom == nil || *rt.Uom != sep2.UomWatts || r.Reading == nil || r.Reading.Value == nil {
		return leg{}, false
	}
	if q := rt.DataQualifier; q != nil && *q != dataQualifierNone && *q != dataQualifierAverage {
		return leg{}, false
	}
	if k := rt.Kind; k != nil && *k != 0 && *k != kindPower {
		return leg{}, false
	}
	if a := rt.AccumulationBehaviour; a != nil && *a != 0 && *a != accumulationIndicating && *a != accumulationInstantaneous {
		return leg{}, false
	}
	l := leg{average: isAverage(rt)}
	if rt.Phase != nil {
		switch *rt.Phase {
		case 0, 224:
		case 128, 129:
			l.phase = phaseA
		case 64, 65:
			l.phase = phaseB
		case 32, 33:
			l.phase = phaseC
		default:
			return leg{}, false
		}
	}
	return l, true
}

// candidate is one leg reading with the hold and index of the mirror it
// came from.
type candidate struct {
	r      sep2.MirrorMeterReading
	hold   int64
	mirror int
}

// legReadings is powerLegs' result. seen marks a leg with any reading whose
// span reaches the window; flagged is set by a reading whose sign cannot be
// mapped (exportPositive).
type legReadings struct {
	spans      map[leg][]powerSpan
	seen       map[leg]bool
	flagged    bool
	concurrent bool
	excluded   int
}

// powerLegs turns a device's DER real-power readings into spans per leg.
//
// An Average reading covers its timePeriod. Without one (intervalLength is
// not decoded by the vendored core) it covers the hold before its receipt,
// back to no earlier than the previous reading of its leg. An instantaneous
// reading holds forward from its receipt until the next reading of its leg,
// for at most the hold. See holdSeconds. Another leg never cuts a span.
//
// Only isDER mirrors count: a premises mirror measures net site power, not
// the DER output a control targets. A reading whose sign cannot be mapped
// yields no span. A W reading legOf rejects is counted as excluded when its
// own span, read alone, reaches the window.
//
// Under 2023 Forward and Reverse are split before overlaps are resolved, as
// newest-wins within one series would discard one direction.
func powerLegs(mirrors []deviceMirror, edition SEP2Edition, ws, we int64) legReadings {
	out := legReadings{spans: map[leg][]powerSpan{}, seen: map[leg]bool{}}
	byLeg := map[leg][]candidate{}
	for mi, m := range mirrors {
		if !m.isDER {
			continue
		}
		hold := holdSeconds(m.postRate)
		for _, r := range m.readings {
			l, ok := legOf(r)
			if !ok {
				if isWatts(r) {
					start, end := readingCoverage([]candidate{{r: r, hold: hold}}, 0)
					if max(start, ws) < min(end, we) {
						out.excluded++
					}
				}
				continue
			}
			if edition == Edition2023 {
				l.dir = directionSeries(r.ReadingType.FlowDirection)
			}
			byLeg[l] = append(byLeg[l], candidate{r: r, hold: hold, mirror: mi})
		}
	}
	covers := map[leg][]mirrorCover{}
	order := 0
	for l, cs := range byLeg {
		slices.SortStableFunc(cs, func(a, b candidate) int { return cmp.Compare(a.r.LastUpdateTime, b.r.LastUpdateTime) })
		base := l
		base.dir = dirMain
		for i, c := range cs {
			start, end := readingCoverage(cs, i)
			start, end = max(start, ws), min(end, we)
			if start >= end {
				continue
			}
			out.seen[l] = true
			covers[base] = append(covers[base], mirrorCover{start: start, end: end, mirror: c.mirror})
			multiplier := int8(0)
			if c.r.ReadingType.PowerOfTenMultiplier != nil {
				multiplier = *c.r.ReadingType.PowerOfTenMultiplier
			}
			watts, mapped := exportPositive(scaledValue(float64(*c.r.Reading.Value), multiplier), c.r.ReadingType.FlowDirection, edition, true)
			if !mapped {
				out.flagged = true
				continue
			}
			out.spans[l] = append(out.spans[l], powerSpan{start: start, end: end, watts: watts, received: c.r.LastUpdateTime, order: order})
			order++
		}
	}
	for _, cv := range covers {
		if mirrorsOverlap(cv) {
			out.concurrent = true
			break
		}
	}
	return out
}

func isWatts(r sep2.MirrorMeterReading) bool {
	return r.ReadingType != nil && r.ReadingType.Uom != nil && *r.ReadingType.Uom == sep2.UomWatts
}

// directionSeries is the 2023 sub-series a reading's flowDirection puts it in.
func directionSeries(flowDirection *uint8) uint8 {
	if flowDirection == nil {
		return dirMain
	}
	switch *flowDirection {
	case sep2.FlowDirectionForward:
		return dirForward
	case sep2.FlowDirectionReverse:
		return dirReverse
	}
	return dirMain
}

// mirrorCover is the window-clipped span one reading of a mirror covers.
type mirrorCover struct {
	start, end int64
	mirror     int
}

// mirrorsOverlap reports whether two different mirrors cover one second.
// Spans come from readingCoverage over the pooled leg, so a client that
// opens a new mirror after a restart, whose last reading's hold is cut by
// the new mirror's first, is not concurrent.
func mirrorsOverlap(cv []mirrorCover) bool {
	slices.SortFunc(cv, func(a, b mirrorCover) int { return cmp.Compare(a.start, b.start) })
	reach := map[int]int64{}
	for _, c := range cv {
		for m, end := range reach {
			if m != c.mirror && end > c.start {
				return true
			}
		}
		reach[c.mirror] = max(reach[c.mirror], c.end)
	}
	return false
}

// readingCoverage is the span cs[i] covers before window clipping. cs is one
// leg, sorted by receipt time.
func readingCoverage(cs []candidate, i int) (start, end int64) {
	r, hold := cs[i].r, cs[i].hold
	at := r.LastUpdateTime
	if !isAverage(r.ReadingType) {
		end = at + hold
		for _, next := range cs[i+1:] {
			if next.r.LastUpdateTime > at {
				end = min(end, next.r.LastUpdateTime)
				break
			}
		}
		return at, end
	}
	if r.Reading != nil && r.Reading.TimePeriod != nil && r.Reading.TimePeriod.Duration > 0 {
		tp := r.Reading.TimePeriod
		return tp.Start, tp.Start + int64(tp.Duration)
	}
	start = at - hold
	for j := i - 1; j >= 0; j-- {
		if cs[j].r.LastUpdateTime < at {
			start = max(start, cs[j].r.LastUpdateTime)
			break
		}
	}
	return start, at
}

func isAverage(rt *sep2.ReadingType) bool {
	return rt.DataQualifier != nil && *rt.DataQualifier == dataQualifierAverage
}

// piece is a stretch of one leg's power, after its overlaps are resolved.
type piece struct {
	start, end int64
	span       powerSpan
}

// resolveSeries splits one leg's spans into disjoint pieces: where its own
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

// integrate values each second by exactly one path, the highest ranked that
// covers it (each leg valued by legValue); paths are never added together:
//  1. the Average total;
//  2. the instantaneous total;
//  3. the phase sum.
//
// Average ranks first because its span is the standard's ("Average over the
// Interval", CSIP Table 3) while the instantaneous hold is inferred; the
// total ranks above the phases because 2018 Table E.2 gives DER active power
// with no phase. The phase sum adds every phase seen in the window, of either
// qualifier, each taking its Average reading where one covers the second and
// its instantaneous one otherwise; phases never overlap, so mixing qualifiers
// cannot double count. It covers a second only when every phase does: a
// dropped phase uncovers the second and is never read as 0 W.
func integrate(spans map[leg][]powerSpan, seen map[leg]bool) (wattSeconds float64, covered int64, used []powerSpan) {
	type edge struct {
		at    int64
		leg   leg
		piece piece
		open  bool
	}
	var edges []edge
	for l, ss := range spans {
		for _, p := range resolveSeries(ss) {
			edges = append(edges, edge{p.start, l, p, true}, edge{p.end, l, p, false})
		}
	}
	// Closes sort before opens at one instant: resolveSeries can split one
	// span into adjacent pieces, and closing the earlier piece after the
	// later one opened would drop the leg.
	slices.SortStableFunc(edges, func(a, b edge) int {
		return cmp.Or(cmp.Compare(a.at, b.at), cmp.Compare(boolInt(a.open), boolInt(b.open)))
	})
	var phases []uint8
	for l := range seen {
		if l.phase != phaseTotal && !slices.Contains(phases, l.phase) {
			phases = append(phases, l.phase)
		}
	}

	// Pieces of one leg are disjoint, so a leg has at most one active piece.
	active := map[leg]powerSpan{}
	usedOrder := map[int]bool{}
	for i := 0; i < len(edges); {
		x := edges[i].at
		for ; i < len(edges) && edges[i].at == x; i++ {
			e := edges[i]
			if e.open {
				active[e.leg] = e.piece.span
			} else if cur, ok := active[e.leg]; ok && cur.order == e.piece.span.order {
				delete(active, e.leg)
			}
		}
		if i == len(edges) {
			break
		}
		path := pickPath(active, seen, phases)
		if len(path) == 0 {
			continue
		}
		seconds := edges[i].at - x
		covered += seconds
		for _, sp := range path {
			wattSeconds += sp.watts * float64(seconds)
			if !usedOrder[sp.order] {
				usedOrder[sp.order] = true
				used = append(used, sp)
			}
		}
	}
	return wattSeconds, covered, used
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// pickPath returns the spans of the highest-ranked path active now, or none.
func pickPath(active map[leg]powerSpan, seen map[leg]bool, phases []uint8) []powerSpan {
	for _, total := range []leg{{average: true}, {average: false}} {
		if sps := legValue(active, seen, total); sps != nil {
			return sps
		}
	}
	if len(phases) == 0 {
		return nil
	}
	var path []powerSpan
	for _, p := range phases {
		sps := legValue(active, seen, leg{average: true, phase: p})
		if sps == nil {
			sps = legValue(active, seen, leg{average: false, phase: p})
		}
		if sps == nil {
			return nil
		}
		path = append(path, sps...)
	}
	return path
}

// legValue returns the spans valuing base now, or nil. Its main series ranks
// first, as a 2023 Net reading is the standard's own |Forward| - |Reverse|
// (2023 FlowDirectionType 4). Otherwise its Forward and Reverse sub-series,
// already export-signed, are summed; once both are seen in the window a
// second needs both, so a dropped direction is uncovered, never read as 0 W.
func legValue(active map[leg]powerSpan, seen map[leg]bool, base leg) []powerSpan {
	if sp, ok := active[base]; ok {
		return []powerSpan{sp}
	}
	fwd, rev := base, base
	fwd.dir, rev.dir = dirForward, dirReverse
	f, fok := active[fwd]
	r, rok := active[rev]
	switch {
	case fok && rok:
		return []powerSpan{f, r}
	case seen[fwd] && seen[rev]:
		return nil
	case fok:
		return []powerSpan{f}
	case rok:
		return []powerSpan{r}
	}
	return nil
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
	lr := powerLegs(mirrors, edition, ws, we)
	d.DirectionUnknown = lr.flagged
	d.ConcurrentMirrors = lr.concurrent
	d.ExcludedReadings = lr.excluded
	wattSeconds, covered, used := integrate(lr.spans, lr.seen)
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
