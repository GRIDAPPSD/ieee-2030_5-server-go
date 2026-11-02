package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ReadingJournalStore is the mirror reading store's journal
// (memory.JournaledScopedStore over MirrorMeterReading).
type ReadingJournalStore interface {
	Sync() error
	Compact(ctx context.Context, keepFrom int64, keep func(parent string, items []sep2.MirrorMeterReading) ([]bool, error)) error
	Parents(ctx context.Context) ([]string, error)
	DeleteParent(ctx context.Context, parentID string) (uint32, error)
}

// ReadingJournal keeps the mirror readings a restart needs on disk
// (GRIDAPPSD/ieee-2030_5-server-go#1206). After a restart the power route
// and its recorder read the same readings an uninterrupted process holds for
// every second they read: the readings that can cover a second from the
// reporting lead of the oldest bucket not yet recorded.
type ReadingJournal struct {
	Readings ReadingJournalStore
	// Mirrors resolves a reading's mirror, for its inline readings.
	Mirrors store.ResourceReader[sep2.MirrorUsagePoint]
}

// Sync flushes the readings appended since the last Sync.
func (j *ReadingJournal) Sync() error { return j.Readings.Sync() }

// Compact rewrites the journal with the readings keepReadings keeps from
// keepFrom. A mirror that cannot be read fails the compaction, so the journal
// keeps everything rather than drop a type a reading inherits.
func (j *ReadingJournal) Compact(ctx context.Context, keepFrom int64) error {
	return j.Readings.Compact(ctx, keepFrom, func(parent string, items []sep2.MirrorMeterReading) ([]bool, error) {
		var inline []sep2.MirrorMeterReading
		switch mup, err := j.Mirrors.Get(ctx, parent); {
		case err == nil:
			inline = mup.MirrorMeterReading
		case !errors.Is(err, store.ErrNotFound):
			return nil, fmt.Errorf("mirror %q: %w", parent, err)
		}
		return keepReadings(inline, items, keepFrom), nil
	})
}

// DropOrphans removes, at boot, the readings of every mirror that no longer
// resolves, so a mirror created later under the same id does not inherit
// them, and logs how many went.
func (j *ReadingJournal) DropOrphans(ctx context.Context) error {
	parents, err := j.Readings.Parents(ctx)
	if err != nil {
		return err
	}
	var mirrors, readings uint32
	for _, p := range parents {
		_, err := j.Mirrors.Get(ctx, p)
		switch {
		case err == nil:
			continue
		case !errors.Is(err, store.ErrNotFound):
			return fmt.Errorf("mirror %q: %w", p, err)
		}
		n, err := j.Readings.DeleteParent(ctx, p)
		if err != nil {
			return err
		}
		mirrors++
		readings += n
	}
	if mirrors > 0 {
		log.Printf("mirror reading journal: dropped %d reading(s) of %d mirror(s) that no longer exist", readings, mirrors)
	}
	return nil
}

// keepReadings is which of one mirror's out-of-band readings a restart needs,
// given its inline ones:
//   - every reading that can cover a second from keepFrom on (readingReach);
//   - the newest reading of each unit, which the fleet route and the power
//     route's non-DER flag read whatever its age;
//   - the typed reading an untyped survivor inherits its ReadingType from
//     (KeepSeriesType).
func keepReadings(inline, items []sep2.MirrorMeterReading, keepFrom int64) []bool {
	typed := InheritReadingTypes(append(slices.Clone(inline), items...))[len(inline):]
	drop := make([]bool, len(items))
	newest := map[uint8]int64{}
	for i, r := range typed {
		drop[i] = readingReach(r) <= keepFrom
		if r.ReadingType != nil && r.ReadingType.Uom != nil {
			u := *r.ReadingType.Uom
			if at, ok := newest[u]; !ok || r.LastUpdateTime > at {
				newest[u] = r.LastUpdateTime
			}
		}
	}
	for i, r := range typed {
		if r.ReadingType != nil && r.ReadingType.Uom != nil && r.LastUpdateTime == newest[*r.ReadingType.Uom] {
			drop[i] = false
		}
	}
	KeepSeriesType(items, drop, inline)
	keep := make([]bool, len(items))
	for i := range drop {
		keep[i] = !drop[i]
	}
	return keep
}

// KeepSeriesType un-drops the oldest typed reading of an mRID when every
// typed reading of it is being dropped while an untyped one survives, out of
// band or inline. A reading that reuses an mRID may omit its ReadingType and
// inherits it from any reading of that mRID, inline ones included (2023 rule
// (n)), so removing the last typed one would leave the surviving series
// untyped and silently out of every figure. readings are one mirror's
// out-of-band readings in store order and drop is one flag per reading.
func KeepSeriesType(readings []sep2.MirrorMeterReading, drop []bool, inline []sep2.MirrorMeterReading) {
	type series struct {
		survivor, typedSurvivor bool
		oldestTypedDropped      int
	}
	byMRID := map[string]*series{}
	for i := range readings {
		m := readings[i].MRID
		s, ok := byMRID[m]
		if !ok {
			s = &series{oldestTypedDropped: -1}
			byMRID[m] = s
		}
		typed := readings[i].ReadingType != nil
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
