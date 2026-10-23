package handler

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log"
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
}

// PowerResponse is the body of GET /api/derms/power. Chart is a
// sep2admin.MarshalChart body, series named by LFDI. TotalGapBuckets counts
// buckets where at least one device has a value and no Total is drawn.
type PowerResponse struct {
	Scope           string          `json:"scope"`
	AggregatorLFDI  string          `json:"aggregatorLFDI,omitempty"`
	Aggregators     []string        `json:"aggregators"`
	WindowStart     int64           `json:"windowStart"`
	WindowEnd       int64           `json:"windowEnd"`
	BucketSeconds   int64           `json:"bucketSeconds"`
	Chart           json.RawMessage `json:"chart"`
	Devices         []PowerDevice   `json:"devices"`
	NotReporting    []string        `json:"notReporting"`
	TotalGapBuckets int             `json:"totalGapBuckets"`
	ReadingsExpired bool            `json:"readingsExpired"`
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
	energy  float64 // watt-seconds over the window, signed
	covered int     // buckets with at least one covered second
	flags   PowerDevice
}

func devicePowerOf(lfdi string, mirrors []deviceMirror, edition SEP2Edition, bounds []int64) devicePower {
	ws, we := bounds[0], bounds[len(bounds)-1]
	lr := powerLegs(mirrors, edition, ws, we)
	sums, _ := integrateBuckets(lr.spans, lr.seen, bounds)
	d := devicePower{lfdi: lfdi, sums: sums}
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

		aggregators := canonicalAll(h.Managers.Managers(ctx))
		slices.Sort(aggregators)
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
			raw, err := fleetMembers(ctx, h.Managers, agg)
			if err != nil {
				log.Printf("admin power: %s", logsafe.Err(err))
				writeError(w, http.StatusInternalServerError, "power read failed, see server log")
				return
			}
			members = canonicalAll(raw)
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
	bounds := make([]int64, 0, window/bucket+1)
	for x := ws; x <= we; x += bucket {
		bounds = append(bounds, x)
	}
	resp.WindowStart, resp.WindowEnd, resp.BucketSeconds = ws, we, bucket
	resp.ReadingsExpired = readingsExpired(ws, now, retention)

	mirrors, err := mirrorReadingsFor(ctx, "admin GET /api/derms/power", h.MirrorUsagePoints, h.MirrorMeterReadings, func(device string) bool {
		return match(canonicalLFDI(device))
	})
	if err != nil {
		return PowerResponse{}, errPowerRead
	}
	byDevice := map[string][]deviceMirror{}
	for _, m := range mirrors {
		byDevice[m.deviceLFDI] = append(byDevice[m.deviceLFDI], m)
	}
	if fleetScope {
		for _, m := range members {
			if _, ok := byDevice[m]; !ok {
				byDevice[m] = nil
			}
		}
	}

	devices := make([]devicePower, 0, len(byDevice))
	for lfdi, ms := range byDevice {
		devices = append(devices, devicePowerOf(lfdi, ms, h.Edition, bounds))
	}
	slices.SortFunc(devices, func(a, b devicePower) int { return cmp.Compare(a.lfdi, b.lfdi) })

	if err := fillPower(&resp, devices, bounds); err != nil {
		log.Printf("admin power: encode chart: %s", logsafe.Err(err))
		return PowerResponse{}, errPowerChart
	}
	return resp, nil
}

// fillPower builds the chart, device list and gap counts of resp from the
// devices' buckets. A Total point needs every reporting device to cover the
// bucket: a total that quietly dropped a device would read as a real power
// change. Of the reporting devices the powerSeriesCap with the largest
// absolute energy are drawn, ties by LFDI; all stay in the Total.
func fillPower(resp *PowerResponse, devices []devicePower, bounds []int64) error {
	resp.Devices = []PowerDevice{}
	resp.NotReporting = []string{}
	var reporting []devicePower
	for _, d := range devices {
		if d.covered > 0 {
			reporting = append(reporting, d)
		} else {
			resp.NotReporting = append(resp.NotReporting, d.lfdi)
		}
	}

	chart := sep2admin.ChartBody{Unit: "W"}
	if len(reporting) > 0 {
		total := sep2admin.ChartSeries{Name: powerTotalName}
		for k := 0; k+1 < len(bounds); k++ {
			covering, sum := 0, 0.0
			for _, d := range reporting {
				if s := d.sums[k]; s.covered > 0 {
					covering++
					sum += s.wattSeconds / float64(s.covered)
				}
			}
			switch {
			case covering == len(reporting):
				total.Points = append(total.Points, sep2admin.ChartPoint{At: time.Unix(bounds[k], 0), Value: sum})
			case covering > 0:
				resp.TotalGapBuckets++
			}
		}
		chart.Series = append(chart.Series, total)
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
