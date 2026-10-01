// Package mirrorretention bounds the out-of-band MirrorMeterReading store
// (POST /mup/{id}/mr): readings older than a retention are removed, and a
// count cap per series (mirror and mRID) removes the oldest beyond it.
// Readings stored inline in a MirrorUsagePoint are not removed.
package mirrorretention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Interval is the period of the sweep that follows the one at boot.
const Interval = 60 * time.Second

// ErrIncomplete is returned by Sweep when a required dependency is missing.
var ErrIncomplete = errors.New("mirrorretention: Retention needs Readings, Mirrors, a positive MaxAge and a positive MaxPerSeries")

// Readings is the part of the reading store the sweep reads and deletes.
type Readings interface {
	Parents(ctx context.Context) ([]string, error)
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.MirrorMeterReading], error)
	Delete(ctx context.Context, parentID, id string) error
}

// Mirrors reads a mirror's inline readings, which take their ReadingType
// from out-of-band records of the same mRID.
type Mirrors interface {
	Get(ctx context.Context, id string) (sep2.MirrorUsagePoint, error)
}

// Retention removes readings whose LastUpdateTime, the server's receipt time,
// is more than MaxAge before the sweep, then the oldest of a series' readings
// beyond MaxPerSeries.
type Retention struct {
	Readings     Readings
	Mirrors      Mirrors
	MaxAge       time.Duration
	MaxPerSeries int
	// Log takes the cap and failure lines and the summary; nil takes
	// slog.Default().
	Log *slog.Logger
}

func (r *Retention) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// record is one stored reading with the store id taken from its href.
type record struct {
	id      string
	reading sep2.MirrorMeterReading
}

// Sweep runs one pass over every mirror and returns how many readings it
// removed. A mirror that cannot be listed, or a reading that cannot be
// deleted, is logged and left for the next sweep; only a failure to list the
// mirrors themselves is returned.
func (r *Retention) Sweep(ctx context.Context, now time.Time) (int, error) {
	if r.Readings == nil || r.Mirrors == nil || r.MaxAge <= 0 || r.MaxPerSeries <= 0 {
		return 0, ErrIncomplete
	}
	parents, err := r.Readings.Parents(ctx)
	if err != nil {
		return 0, fmt.Errorf("mirrorretention: list mirrors: %w", err)
	}
	cutoff := now.Add(-r.MaxAge).Unix()
	removed, failures := 0, 0
	for _, mupID := range parents {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		n, f := r.sweepMirror(ctx, mupID, cutoff)
		removed += n
		failures += f
	}
	if removed > 0 || failures > 0 {
		r.logger().Info("mirrorretention: readings swept", "removed", removed, "skipped", failures)
	}
	return removed, ctx.Err()
}

// sweepMirror applies both bounds to one mirror's readings, oldest first.
func (r *Retention) sweepMirror(ctx context.Context, mupID string, cutoff int64) (removed, failures int) {
	var inline []sep2.MirrorMeterReading
	switch mup, err := r.Mirrors.Get(ctx, mupID); {
	case err == nil:
		inline = mup.MirrorMeterReading
	case errors.Is(err, store.ErrNotFound):
	default:
		// Without the inline readings the type rule cannot be applied, so
		// nothing of this mirror is removed.
		r.logger().Warn("mirrorretention: left for the next sweep", "mirror", mupID, "err", err)
		return 0, 1
	}
	page, err := r.Readings.List(ctx, mupID, store.ListOptions{Unbounded: true})
	if err != nil {
		r.logger().Warn("mirrorretention: left for the next sweep", "mirror", mupID, "err", err)
		return 0, 1
	}
	recs := make([]record, 0, len(page.Items))
	for _, m := range page.Items {
		id, ok := readingID(mupID, m.Href)
		if !ok {
			// The id is only known from the server-stamped href; a record
			// without one cannot be addressed, so it is left, not guessed at.
			r.logger().Warn("mirrorretention: reading href is not under its mirror, left in place", "mirror", mupID, "href", m.Href)
			failures++
			continue
		}
		recs = append(recs, record{id: id, reading: m})
	}

	drop := make([]bool, len(recs))
	for i := range recs {
		drop[i] = recs[i].reading.LastUpdateTime < cutoff
	}
	// Ids are fixed-width nanoseconds, so store order is age order and the
	// cap takes each series' oldest survivors first.
	perSeries := map[string]int{}
	for i := range recs {
		if !drop[i] {
			perSeries[recs[i].reading.MRID]++
		}
	}
	overCap := map[string]int{}
	for i := range recs {
		m := recs[i].reading.MRID
		if !drop[i] && perSeries[m] > r.MaxPerSeries {
			drop[i] = true
			perSeries[m]--
			overCap[m]++
		}
	}
	keepSeriesType(recs, drop, inline)

	for i := range recs {
		if !drop[i] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return removed, failures
		}
		err := r.Readings.Delete(ctx, mupID, recs[i].id)
		switch {
		case err == nil:
			removed++
		case errors.Is(err, store.ErrNotFound):
		default:
			r.logger().Warn("mirrorretention: left for the next sweep", "mirror", mupID, "reading", recs[i].id, "err", err)
			failures++
		}
	}
	for _, m := range slices.Sorted(maps.Keys(overCap)) {
		r.logger().Warn("mirrorretention: series over its reading cap, oldest removed",
			"mirror", mupID, "mrid", m, "overCap", overCap[m], "maxPerSeries", r.MaxPerSeries)
	}
	return removed, failures
}

// keepSeriesType un-drops the oldest typed record of an mRID when every typed
// record of it is being dropped while an untyped one survives, out of band or
// inline. A reading that reuses an mRID may omit its ReadingType and inherits
// it from any record of that mRID, inline ones included (2023 rule (n)), so
// removing the last typed one would leave the surviving series untyped and
// silently out of every figure.
func keepSeriesType(recs []record, drop []bool, inline []sep2.MirrorMeterReading) {
	type series struct {
		survivor, typedSurvivor bool
		oldestTypedDropped      int
	}
	byMRID := map[string]*series{}
	for i := range recs {
		m := recs[i].reading.MRID
		s, ok := byMRID[m]
		if !ok {
			s = &series{oldestTypedDropped: -1}
			byMRID[m] = s
		}
		typed := recs[i].reading.ReadingType != nil
		switch {
		case !drop[i]:
			s.survivor = true
			s.typedSurvivor = s.typedSurvivor || typed
		case typed && s.oldestTypedDropped < 0:
			s.oldestTypedDropped = i
		}
	}
	for _, m := range inline {
		if s, ok := byMRID[m.MRID]; ok {
			s.survivor = true
			s.typedSurvivor = s.typedSurvivor || m.ReadingType != nil
		}
	}
	for _, s := range byMRID {
		if s.survivor && !s.typedSurvivor && s.oldestTypedDropped >= 0 {
			drop[s.oldestTypedDropped] = false
		}
	}
}

// readingID returns the store id of a reading stamped /mup/{mupID}/mr/{id}.
func readingID(mupID, href string) (string, bool) {
	id, ok := strings.CutPrefix(href, "/mup/"+mupID+"/mr/")
	if !ok || id == "" || strings.Contains(id, "/") {
		return "", false
	}
	return id, true
}

// Start runs Sweep every interval, with the time from now, until ctx ends or
// the returned stop is called. An interval of zero or less takes Interval.
// stop cancels a sweep in progress, waits for it to return, and is safe to
// call more than once.
func (r *Retention) Start(ctx context.Context, interval time.Duration, now func() time.Time) (stop func()) {
	if interval <= 0 {
		interval = Interval
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if _, err := r.Sweep(ctx, now()); err != nil && ctx.Err() == nil {
					r.logger().Error("mirrorretention: sweep failed, retried at the next tick", "err", err)
				}
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			<-done
		})
	}
}
