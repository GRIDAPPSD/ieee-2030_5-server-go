package handler

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log"
	"maps"
	"math"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
)

// GET /api/derms/power: export-positive real power per time bucket for one
// aggregator's fleet or for every device, as a chart of a Total line plus the
// largest devices. Power is read exactly as the DER control delivery figure
// reads it (powerLegs, integrateBuckets), so the two never disagree.

// powerSeriesCap is MaxChartSeries less the Total line.
const powerSeriesCap = sep2admin.MaxChartSeries - 1

const powerTotalName = "Total"

// The energy chart's series: export-positive, so a PV fleet's generation is
// Discharged too.
const (
	powerEnergyDischarged = "Discharged"
	powerEnergyCharged    = "Charged"
)

// wattSecondsPerKWh converts watt-seconds to kilowatt-hours.
const wattSecondsPerKWh = 3.6e6

// powerBucketCap is the most buckets one window may split into.
const powerBucketCap = sep2admin.MaxChartSeriesPoints

// powerBucketSizes are the bucket lengths in seconds, smallest first: the
// sizes the allowed windows select (5 s for 15 min and 1 h, 30 s for 6 h,
// 120 s for 24 h).
var powerBucketSizes = []int64{5, 30, 120}

// powerWindows are the accepted window lengths in seconds: 15 min, 1 h, 6 h
// and 24 h, the last inside the default 25 h reading retention.
var powerWindows = []int64{900, 3600, 21600, 86400}

// powerCacheTTL is how long an answer is reused: the smallest bucket, so a
// cached answer is at most one bucket behind the answer a fresh read would
// give. Polls arrive once per bucket per open page, so the cache mainly
// collapses concurrent pages onto one read of the mirror stores.
const powerCacheTTL = 5 * time.Second

var (
	errPowerRead  = errors.New("admin power: mirror read failed")
	errPowerChart = errors.New("admin power: chart refused")
)

// powerKey names everything an answer depends on besides the clock. The
// aggregator is the canonical LFDI, empty for the all-devices scope.
type powerKey struct {
	scope, aggregator string
	window            int64
}

// powerEntry is one answer, in flight until done is closed. at, resp and err
// are written before the close and read only after it.
type powerEntry struct {
	done chan struct{}
	at   time.Time
	resp PowerResponse
	err  error
}

// powerCache holds one answer per powerKey for ttl and lets concurrent
// callers of one key share a single computation. A failed computation is
// handed to the callers waiting on it and never kept.
type powerCache struct {
	mu      sync.Mutex
	entries map[powerKey]*powerEntry
	ttl     time.Duration
	clock   func() time.Time
}

func newPowerCache(ttl time.Duration) *powerCache {
	return &powerCache{entries: map[powerKey]*powerEntry{}, ttl: ttl, clock: time.Now}
}

// usableLocked reports whether e is in flight or a fresh success.
func (c *powerCache) usableLocked(e *powerEntry) bool {
	select {
	case <-e.done:
		return e.err == nil && c.clock().Sub(e.at) < c.ttl
	default:
		return true
	}
}

func (c *powerCache) get(ctx context.Context, key powerKey, compute func() (PowerResponse, error)) (PowerResponse, error) {
	c.mu.Lock()
	if e, ok := c.entries[key]; ok && c.usableLocked(e) {
		c.mu.Unlock()
		select {
		case <-e.done:
			return e.resp, e.err
		case <-ctx.Done():
			return PowerResponse{}, ctx.Err()
		}
	}
	for k, e := range c.entries {
		if !c.usableLocked(e) {
			delete(c.entries, k)
		}
	}
	e := &powerEntry{done: make(chan struct{}), err: errPowerRead}
	c.entries[key] = e
	c.mu.Unlock()

	defer func() {
		e.at = c.clock()
		if e.err != nil {
			c.mu.Lock()
			if c.entries[key] == e {
				delete(c.entries, key)
			}
			c.mu.Unlock()
		}
		close(e.done)
	}()
	e.resp, e.err = compute()
	return e.resp, e.err
}

// PowerDevice is one device's entry in a power answer. Drawn is false for a
// device reporting power that did not make the series cap, and for one with
// no covered second. NonDERWatts marks a device with W readings on a mirror
// that is not flagged isDER, which this route never plots.
type PowerDevice struct {
	LFDI              string `json:"lfdi"`
	Drawn             bool   `json:"drawn"`
	CoveredBuckets    int    `json:"coveredBuckets"`
	DirectionUnknown  bool   `json:"directionUnknown"`
	ConcurrentMirrors bool   `json:"concurrentMirrors"`
	ExcludedReadings  int    `json:"excludedReadings"`
	NonDERWatts       bool   `json:"nonDERWatts"`
	// LatestW is the device's power now in watts, export-positive, and
	// LatestAt the end of the 5 s bucket it was read from; both are null when
	// the device has no covered second in the last latestSpanSeconds.
	LatestW  *float64 `json:"latestW"`
	LatestAt *int64   `json:"latestAt"`
}

// PowerResponse is the body of GET /api/derms/power. Chart is a
// sep2admin.MarshalChart body, series named by LFDI. TotalGapBuckets counts
// buckets where at least one device has a value and no Total is drawn.
// EnergyChart is a sep2admin.MarshalChart body in kWh per bucket with a
// Discharged series (values at or above zero) and a Charged series (at or
// below zero); each has a point exactly where Chart's Total does.
type PowerResponse struct {
	Scope           string          `json:"scope"`
	AggregatorLFDI  string          `json:"aggregatorLFDI,omitempty"`
	Aggregators     []string        `json:"aggregators"`
	WindowStart     int64           `json:"windowStart"`
	WindowEnd       int64           `json:"windowEnd"`
	BucketSeconds   int64           `json:"bucketSeconds"`
	Chart           json.RawMessage `json:"chart"`
	EnergyChart     json.RawMessage `json:"energyChart"`
	Devices         []PowerDevice   `json:"devices"`
	NotReporting    []string        `json:"notReporting"`
	TotalGapBuckets int             `json:"totalGapBuckets"`
	ReadingsExpired bool            `json:"readingsExpired"`
	// LatestW is the sum of every device's LatestW, in watts, and LatestAt the
	// oldest bucket end among them. Both are null while LatestMissing is not
	// empty, or when no device has a value: a partial sum would read as a
	// real power change. LatestMissing lists the devices that covered a second
	// of the last latestReportedSeconds but have none in the last
	// latestSpanSeconds, whatever the chart window.
	LatestW       *float64 `json:"latestW"`
	LatestAt      *int64   `json:"latestAt"`
	LatestMissing []string `json:"latestMissing"`
}

// powerBucketSeconds is the smallest bucket that keeps window within
// powerBucketCap buckets.
func powerBucketSeconds(window int64) int64 {
	for _, b := range powerBucketSizes {
		if window/b <= powerBucketCap {
			return b
		}
	}
	return powerBucketSizes[len(powerBucketSizes)-1]
}

// devicePower is one device's integrated buckets and its flags.
type devicePower struct {
	lfdi    string
	sums    []bucketSum
	signed  []bucketSigned
	energy  float64 // watt-seconds over the window, signed
	covered int     // buckets with at least one covered second
	flags   PowerDevice
}

func devicePowerOf(lfdi string, mirrors []deviceMirror, edition SEP2Edition, bounds []int64) devicePower {
	ws, we := bounds[0], bounds[len(bounds)-1]
	lr := powerLegs(mirrors, edition, ws, we)
	sums, signed, _ := integrateSigned(lr.spans, lr.seen, bounds)
	d := devicePower{lfdi: lfdi, sums: sums, signed: signed}
	for _, s := range sums {
		if s.covered > 0 {
			d.covered++
			d.energy += s.wattSeconds
		}
	}
	d.flags = PowerDevice{
		LFDI:              lfdi,
		CoveredBuckets:    d.covered,
		DirectionUnknown:  lr.flagged,
		ConcurrentMirrors: lr.concurrent,
		ExcludedReadings:  lr.excluded,
	}
	for _, m := range mirrors {
		if m.isDER {
			continue
		}
		for _, r := range m.readings {
			if isWatts(r) {
				d.flags.NonDERWatts = true
			}
		}
	}
	return d
}

// HandlePower returns the handler for GET /api/derms/power. The query is
// window (900, 3600, 21600 or 86400 seconds) and exactly one of
// aggregatorLFDI and scope=all. h supplies the management pairs, the mirror
// stores and the edition; retention is the mirror reading retention, zero
// when unknown. The route sits under the sensitive /api/derms/ prefix, so a
// real credential is required even from loopback.
func HandlePower(h *AdminFleetHandler, retention time.Duration) http.HandlerFunc {
	cache := newPowerCache(powerCacheTTL)
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		q := r.URL.Query()
		window, err := strconv.ParseInt(q.Get("window"), 10, 64)
		if err != nil || !slices.Contains(powerWindows, window) {
			writeError(w, http.StatusBadRequest, "window must be 900, 3600, 21600 or 86400 seconds")
			return
		}
		scope, fleetScope := q.Get("scope"), q.Has("aggregatorLFDI")
		if fleetScope == (scope != "") || (scope != "" && scope != "all") {
			writeError(w, http.StatusBadRequest, "give exactly one of aggregatorLFDI and scope=all")
			return
		}

		aggregators := sortedAggregators(ctx, h)
		resp := PowerResponse{Scope: "all", Aggregators: aggregators}
		match := func(string) bool { return true }
		var members []string
		if fleetScope {
			agg, ok := sep2lfdi.CanonicalTrimSpace(q.Get("aggregatorLFDI"))
			if !ok {
				writeError(w, http.StatusBadRequest, "aggregatorLFDI must be 40 hex digits")
				return
			}
			if !slices.Contains(aggregators, agg) {
				writeError(w, http.StatusNotFound, "no such aggregator")
				return
			}
			members, err = canonicalMembers(ctx, h, agg)
			if err != nil {
				log.Printf("admin power: %s", logsafe.Err(err))
				writeError(w, http.StatusInternalServerError, "power read failed, see server log")
				return
			}
			resp.Scope, resp.AggregatorLFDI = "fleet", agg
			match = func(device string) bool { return slices.Contains(members, device) }
		}

		key := powerKey{scope: resp.Scope, aggregator: resp.AggregatorLFDI, window: window}
		out, err := cache.get(ctx, key, func() (PowerResponse, error) {
			return computePower(context.WithoutCancel(ctx), h, retention, resp, fleetScope, members, match, window)
		})
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, out)
		case errors.Is(err, errPowerChart):
			writeError(w, http.StatusInternalServerError, "power chart refused, see server log")
		default:
			writeError(w, http.StatusInternalServerError, "power read failed, see server log")
		}
	}
}

// computePower reads the mirrors and builds one answer. resp arrives with
// its scope, aggregator and aggregator list set. The read runs without the
// caller's cancellation: its answer is shared with every poll that joined it.
func computePower(ctx context.Context, h *AdminFleetHandler, retention time.Duration, resp PowerResponse, fleetScope bool, members []string, match func(string) bool, window int64) (PowerResponse, error) {
	now := nowUnix()
	bucket := powerBucketSeconds(window)
	we := now / bucket * bucket
	ws := we - window
	bounds := boundsFor(ws, we, bucket)
	resp.WindowStart, resp.WindowEnd, resp.BucketSeconds = ws, we, bucket
	resp.ReadingsExpired = readingsExpired(ws, now, retention)

	byDevice, err := mirrorsByDevice(ctx, h, "admin GET /api/derms/power", match)
	if err != nil {
		return PowerResponse{}, errPowerRead
	}
	if fleetScope {
		for _, m := range members {
			if _, ok := byDevice[m]; !ok {
				byDevice[m] = nil
			}
		}
	}

	latestBounds := latestGrid(now)
	latestEnd := latestBounds[len(latestBounds)-1]
	reportedBounds := []int64{reportedLead(latestEnd), latestEnd}
	grid := fineGrid(ws, we)
	devices := make([]devicePower, 0, len(byDevice))
	fine := make([]devicePower, 0, len(byDevice))
	latest := make([]devicePower, 0, len(byDevice))
	reported := make([]devicePower, 0, len(byDevice))
	for _, lfdi := range slices.Sorted(maps.Keys(byDevice)) {
		ms := byDevice[lfdi]
		devices = append(devices, devicePowerOf(lfdi, ms, h.Edition, bounds))
		fine = append(fine, devicePowerOf(lfdi, ms, h.Edition, grid))
		latest = append(latest, devicePowerOf(lfdi, ms, h.Edition, latestBounds))
		reported = append(reported, devicePowerOf(lfdi, ms, h.Edition, reportedBounds))
	}

	historyScope := powerHistoryAllScope
	if fleetScope {
		historyScope = resp.AggregatorLFDI
	}
	live := fineSource(fineTotals(fine, len(grid)-1), ws)
	if err := fillPowerFrom(&resp, devices, bounds, live, h.PowerHistory.view(historyScope, now)); err != nil {
		log.Printf("admin power: encode chart: %s", logsafe.Err(err))
		return PowerResponse{}, errPowerChart
	}
	latestPower(&resp, reported, latest, latestBounds)
	return resp, nil
}

// latestSpanSeconds is how far back a device's power now may be read: the
// longest hold, so an Average-only device still has a value between posts.
const latestSpanSeconds = maxHoldSeconds

// latestReportedSeconds is how far back a device must have covered a second
// to count as reporting for the total, whatever the chart window: a device
// in that span with no value in the last latestSpanSeconds is missing.
const latestReportedSeconds = 3600

// reportedLead is where the latestReportedSeconds before end begin.
func reportedLead(end int64) int64 { return end - latestReportedSeconds }

// latestGrid is the 5 s grid over the latestSpanSeconds before now, ending
// on the last closed 5 s boundary. It does not depend on the chart window,
// so the number does not change when the operator changes the window.
func latestGrid(now int64) []int64 {
	end := now / 5 * 5
	return boundsFor(end-latestSpanSeconds, end, 5)
}

// boundsFor is the bucket boundaries from start to end, step seconds apart.
func boundsFor(start, end, step int64) []int64 {
	bounds := make([]int64, 0, (end-start)/step+1)
	for x := start; x <= end; x += step {
		bounds = append(bounds, x)
	}
	return bounds
}

// latestPower is the one owner of a device's power now. latest holds each
// device integrated over bounds by devicePowerOf, so the reading rule is the
// chart's (powerLegs, pickPath) and not a second one. A device's value is
// the average W of its newest bucket with a covered second. It fills
// resp.Devices' LatestW and LatestAt and the response's total, which needs a
// value from every device in reported that covered a second of the last
// latestReportedSeconds.
func latestPower(resp *PowerResponse, reported, latest []devicePower, bounds []int64) {
	type value struct {
		w  float64
		at int64
	}
	values := map[string]value{}
	for _, d := range latest {
		for k := len(d.sums) - 1; k >= 0; k-- {
			if s := d.sums[k]; s.covered > 0 {
				values[d.lfdi] = value{w: s.wattSeconds / float64(s.covered), at: bounds[k+1]}
				break
			}
		}
	}
	for i := range resp.Devices {
		if v, ok := values[resp.Devices[i].LFDI]; ok {
			resp.Devices[i].LatestW, resp.Devices[i].LatestAt = &v.w, &v.at
		}
	}
	resp.LatestMissing = []string{}
	for _, d := range reported {
		if _, ok := values[d.lfdi]; d.covered > 0 && !ok {
			resp.LatestMissing = append(resp.LatestMissing, d.lfdi)
		}
	}
	if len(resp.LatestMissing) > 0 || len(values) == 0 {
		return
	}
	var sum float64
	oldest := int64(math.MaxInt64)
	for _, d := range latest {
		if v, ok := values[d.lfdi]; ok {
			sum += v.w
			oldest = min(oldest, v.at)
		}
	}
	resp.LatestW, resp.LatestAt = &sum, &oldest
}

// reportingOf splits devices into those with a covered second in their
// buckets and the LFDIs of those without.
func reportingOf(devices []devicePower) (reporting []devicePower, notReporting []string) {
	notReporting = []string{}
	for _, d := range devices {
		if d.covered > 0 {
			reporting = append(reporting, d)
		} else {
			notReporting = append(notReporting, d.lfdi)
		}
	}
	return reporting, notReporting
}

// fillPower is fillPowerFrom with the devices' own buckets as the totals and
// no history: a Total point needs every device with a covered second in the
// buckets to cover the bucket.
func fillPower(resp *PowerResponse, devices []devicePower, bounds []int64) error {
	reporting, _ := reportingOf(devices)
	live := func(start, end int64) (bucketTotal, historyState) {
		t := totalBucket(reporting, slices.Index(bounds, start), float64(end-start))
		if state := t.state(); state != historyCovered {
			return bucketTotal{}, state
		}
		return t, historyCovered
	}
	return fillPowerFrom(resp, devices, bounds, live, nil)
}

// fillPowerFrom builds the chart, device list and gap counts of resp. The
// Total and energy points come from live, which totals the route bucket
// [start, end) by the 5 s rule the recorder also uses (fineSource), so a
// recorded bucket equals the live one; a Total point needs every reporting
// device to cover the bucket, since a total that quietly dropped a device
// would read as a real power change. history, when not nil, is asked first,
// and what it has recorded for a bucket replaces the live answer, so the
// series survive the readings that made them. Of the reporting devices the
// powerSeriesCap with the largest absolute energy are drawn, ties by LFDI;
// all stay in the Total.
func fillPowerFrom(resp *PowerResponse, devices []devicePower, bounds []int64, live, history func(start, end int64) (bucketTotal, historyState)) error {
	resp.Devices = []PowerDevice{}
	reporting, notReporting := reportingOf(devices)
	resp.NotReporting = notReporting

	chart := sep2admin.ChartBody{Unit: "W"}
	energy := sep2admin.ChartBody{Unit: "kWh"}
	total := sep2admin.ChartSeries{Name: powerTotalName}
	discharged := sep2admin.ChartSeries{Name: powerEnergyDischarged}
	charged := sep2admin.ChartSeries{Name: powerEnergyCharged}
	answered := false
	for k := 0; k+1 < len(bounds); k++ {
		t, state := live(bounds[k], bounds[k+1])
		if history != nil {
			if ht, hs := history(bounds[k], bounds[k+1]); hs != historyNone {
				t, state = ht, hs
			}
		}
		switch state {
		case historyCovered:
			answered = true
			at := time.Unix(bounds[k], 0)
			total.Points = append(total.Points, sep2admin.ChartPoint{At: at, Value: t.w})
			discharged.Points = append(discharged.Points, sep2admin.ChartPoint{At: at, Value: t.exportWs / wattSecondsPerKWh})
			charged.Points = append(charged.Points, sep2admin.ChartPoint{At: at, Value: t.importWs / wattSecondsPerKWh})
		case historyGap:
			answered = true
			resp.TotalGapBuckets++
		}
	}
	if len(reporting) > 0 || answered {
		chart.Series = append(chart.Series, total)
		energy.Series = append(energy.Series, discharged, charged)
	}

	ranked := slices.Clone(reporting)
	slices.SortStableFunc(ranked, func(a, b devicePower) int {
		return cmp.Or(cmp.Compare(math.Abs(b.energy), math.Abs(a.energy)), cmp.Compare(a.lfdi, b.lfdi))
	})
	drawn := map[string]bool{}
	for _, d := range ranked[:min(len(ranked), powerSeriesCap)] {
		drawn[d.lfdi] = true
		s := sep2admin.ChartSeries{Name: d.lfdi}
		for k, b := range d.sums {
			if b.covered > 0 {
				s.Points = append(s.Points, sep2admin.ChartPoint{At: time.Unix(bounds[k], 0), Value: b.wattSeconds / float64(b.covered)})
			}
		}
		chart.Series = append(chart.Series, s)
	}
	for _, d := range devices {
		d.flags.Drawn = drawn[d.lfdi]
		resp.Devices = append(resp.Devices, d.flags)
	}

	body, err := sep2admin.MarshalChart(chart)
	if err != nil {
		return err
	}
	resp.Chart = body
	if resp.EnergyChart, err = sep2admin.MarshalChart(energy); err != nil {
		return err
	}
	return nil
}

// canonicalLFDI is s in canonical form, or s as stored when it is not 40 hex
// digits, so a malformed stored value still groups with itself.
func canonicalLFDI(s string) string {
	if c, ok := sep2lfdi.Canonical(s); ok {
		return c
	}
	return s
}

func canonicalAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, canonicalLFDI(s))
	}
	return out
}

// sortedAggregators is the canonical LFDI of every aggregator, sorted.
func sortedAggregators(ctx context.Context, h *AdminFleetHandler) []string {
	aggregators := canonicalAll(h.Managers.Managers(ctx))
	slices.Sort(aggregators)
	return aggregators
}

// canonicalMembers is the canonical LFDI of an aggregator's fleet: itself and
// the devices it manages.
func canonicalMembers(ctx context.Context, h *AdminFleetHandler, aggregator string) ([]string, error) {
	raw, err := fleetMembers(ctx, h.Managers, aggregator)
	if err != nil {
		return nil, err
	}
	return canonicalAll(raw), nil
}

// mirrorsByDevice reads the mirrors of the devices match accepts (by
// canonical LFDI), grouped by the mirror's device.
func mirrorsByDevice(ctx context.Context, h *AdminFleetHandler, label string, match func(canonical string) bool) (map[string][]deviceMirror, error) {
	mirrors, err := mirrorReadingsFor(ctx, label, h.MirrorUsagePoints, h.MirrorMeterReadings, func(device string) bool {
		return match(canonicalLFDI(device))
	})
	if err != nil {
		return nil, err
	}
	byDevice := map[string][]deviceMirror{}
	for _, m := range mirrors {
		byDevice[m.deviceLFDI] = append(byDevice[m.deviceLFDI], m)
	}
	return byDevice, nil
}
