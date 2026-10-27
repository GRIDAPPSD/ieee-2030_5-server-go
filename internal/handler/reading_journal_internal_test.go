package handler

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func journalReading(mrid string, at int64, rt *sep2.ReadingType) sep2.MirrorMeterReading {
	v := int64(1)
	return sep2.MirrorMeterReading{MRID: mrid, LastUpdateTime: at, Reading: &sep2.Reading{Value: &v}, ReadingType: rt}
}

func voltsType() *sep2.ReadingType {
	uom := sep2.UomVolts
	return &sep2.ReadingType{Uom: &uom}
}

func TestKeepReadings(t *testing.T) {
	const keepFrom = 10_000
	w := twinType(sep2.FlowDirectionReverse, false)
	period := journalReading("P", 5000, twinType(sep2.FlowDirectionReverse, true))
	period.Reading.TimePeriod = &sep2.DateTimeInterval{Start: 9000, Duration: 1001}
	items := []sep2.MirrorMeterReading{
		journalReading("M", 1000, w),                      // 0: the type an untyped M survivor inherits
		journalReading("N", 1000, w),                      // 1: N's type is also held inline
		journalReading("V", 1500, voltsType()),            // 2: an older V
		journalReading("V", 2000, voltsType()),            // 3: the newest V, however old
		period,                                            // 4: received long ago, period ends after keepFrom
		journalReading("W", keepFrom-maxHoldSeconds, w),   // 5: reaches exactly keepFrom
		journalReading("W", keepFrom-maxHoldSeconds+1, w), // 6: reaches one second past it
		journalReading("M", 15_000, nil),                  // 7: untyped, inherits from 0
		journalReading("N", 15_000, nil),                  // 8: untyped, inherits from the inline N
		journalReading("W", 20_000, w),                    // 9: the newest W
	}
	inline := []sep2.MirrorMeterReading{journalReading("N", 500, w)}
	got := keepReadings(inline, items, keepFrom)
	want := []bool{true, false, false, true, true, false, true, true, true, true}
	if !slices.Equal(got, want) {
		t.Errorf("keep = %v, want %v", got, want)
	}

	// The period reading one second short of keepFrom is dropped too.
	period.Reading.TimePeriod.Duration = 1000
	items[4] = period
	if keepReadings(inline, items, keepFrom)[4] {
		t.Error("a period ending at keepFrom is kept")
	}
}

type failingMirrors struct {
	store.ResourceReader[sep2.MirrorUsagePoint]
}

func (failingMirrors) Get(context.Context, string) (sep2.MirrorUsagePoint, error) {
	return sep2.MirrorUsagePoint{}, errors.New("mirror store down")
}

// A compaction that cannot read a mirror changes nothing, and orphaned
// readings go at boot while a resolving mirror's stay.
func TestReadingJournal_CompactFailsClosedAndOrphansGo(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "readings.jsonl")
	js, err := memory.NewJournaledScopedStore[sep2.MirrorMeterReading](path, "MirrorMeterReading", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{"1", "2"} {
		if err := js.Create(ctx, parent, "a", journalReading("W", 100, twinType(sep2.FlowDirectionReverse, false))); err != nil {
			t.Fatal(err)
		}
		if err := js.Create(ctx, parent, "b", journalReading("W", 200, twinType(sep2.FlowDirectionReverse, false))); err != nil {
			t.Fatal(err)
		}
	}
	j := &ReadingJournal{Readings: js, Mirrors: failingMirrors{}}
	if err := j.Compact(ctx, 1_000_000); err == nil {
		t.Fatal("compaction with an unreadable mirror succeeded")
	}
	if err := j.DropOrphans(ctx); err == nil {
		t.Fatal("orphan drop with an unreadable mirror succeeded")
	}
	reopened := func() *memory.JournaledScopedStore[sep2.MirrorMeterReading] {
		t.Helper()
		s, err := memory.NewJournaledScopedStore[sep2.MirrorMeterReading](path, "MirrorMeterReading", 1)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, parent := range []string{"1", "2"} {
		if n, _ := reopened().Count(ctx, parent); n != 2 {
			t.Fatalf("parent %s holds %d readings after a failed compaction, want 2", parent, n)
		}
	}

	mups := memory.NewStore[sep2.MirrorUsagePoint]()
	if err := mups.Create(ctx, "1", sep2.MirrorUsagePoint{}); err != nil {
		t.Fatal(err)
	}
	j = &ReadingJournal{Readings: js, Mirrors: mups}
	if err := j.DropOrphans(ctx); err != nil {
		t.Fatal(err)
	}
	// Compaction keeps the newest W of mirror 1 only.
	if err := j.Compact(ctx, 1_000_000); err != nil {
		t.Fatal(err)
	}
	after := reopened()
	if n, _ := after.Count(ctx, "2"); n != 0 {
		t.Errorf("orphaned mirror 2 holds %d readings after a restart, want 0", n)
	}
	page, err := after.List(ctx, "1", store.ListOptions{Unbounded: true})
	if err != nil || len(page.Items) != 1 || page.Items[0].LastUpdateTime != 200 {
		t.Errorf("mirror 1 after compaction and a restart = %+v, %v; want the reading at 200 only", page.Items, err)
	}
	if n, _ := js.Count(ctx, "1"); n != 2 {
		t.Errorf("compaction removed readings from memory: mirror 1 holds %d, want 2", n)
	}
}
