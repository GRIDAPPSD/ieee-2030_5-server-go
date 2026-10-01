package mirrorretention_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mirrorretention"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

var sweepNow = time.Unix(1_800_000_000, 0)

// put stores a reading the way POST /mup/{id}/mr does: the id is the
// fixed-width receipt nanoseconds, and href and lastUpdateTime derive from it.
func put(t *testing.T, s *memory.ScopedStore[sep2.MirrorMeterReading], mupID string, nanos int64, mrid string, rt *sep2.ReadingType) string {
	t.Helper()
	id := fmt.Sprintf("%020d", nanos)
	m := sep2.MirrorMeterReading{MRID: mrid, LastUpdateTime: time.Unix(0, nanos).Unix(), ReadingType: rt}
	m.Href = "/mup/" + mupID + "/mr/" + id
	if err := s.Create(context.Background(), mupID, id, m); err != nil {
		t.Fatal(err)
	}
	return id
}

func secondsAgo(s int64, offset int64) int64 {
	return sweepNow.Add(-time.Duration(s)*time.Second).UnixNano() + offset
}

func stored(t *testing.T, s *memory.ScopedStore[sep2.MirrorMeterReading], mupID string) []sep2.MirrorMeterReading {
	t.Helper()
	page, err := s.List(context.Background(), mupID, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func watts() *sep2.ReadingType {
	u := sep2.UomWatts
	return &sep2.ReadingType{Uom: &u}
}

func defaults() *mirrorretention.Retention {
	return &mirrorretention.Retention{
		Mirrors:      memory.NewStore[sep2.MirrorUsagePoint](),
		MaxAge:       config.DefaultMirrorReadingRetention,
		MaxPerSeries: config.DefaultMirrorReadingMaxPerSeries,
		Log:          slog.New(slog.DiscardHandler),
	}
}

// G/W/T 1: only the reading older than the retention goes.
func TestSweep_RemovesOnlyReadingsOlderThanRetention(t *testing.T) {
	s := memory.NewScopedStore[sep2.MirrorMeterReading]()
	put(t, s, "1", secondsAgo(25*3600+1, 0), "W", watts())
	young := put(t, s, "1", secondsAgo(25*3600-1, 0), "W", watts())

	r := defaults()
	r.Readings = s
	n, err := r.Sweep(context.Background(), sweepNow)
	if err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v, want 1 removed", n, err)
	}
	got := stored(t, s, "1")
	if len(got) != 1 || got[0].Href != "/mup/1/mr/"+young || got[0].LastUpdateTime != sweepNow.Unix()-(25*3600-1) {
		t.Fatalf("stored = %+v, want only the 25 h - 1 s reading %s", got, young)
	}
}

// A reading exactly as old as the retention is kept.
func TestSweep_KeepsReadingAtRetention(t *testing.T) {
	s := memory.NewScopedStore[sep2.MirrorMeterReading]()
	put(t, s, "1", secondsAgo(25*3600, 0), "W", watts())
	r := defaults()
	r.Readings = s
	if n, err := r.Sweep(context.Background(), sweepNow); err != nil || n != 0 || len(stored(t, s, "1")) != 1 {
		t.Fatalf("Sweep = %d, %v, want the reading at the retention kept", n, err)
	}
}

// G/W/T 2: over the cap the oldest of the series goes, and one log line names
// the mirror and the series.
func TestSweep_CapRemovesOldestAndLogsMirror(t *testing.T) {
	s := memory.NewScopedStore[sep2.MirrorMeterReading]()
	const n = 20001
	ids := make([]string, n)
	for i := range n {
		ids[i] = put(t, s, "m7", secondsAgo(3600, int64(i)), "W", watts())
	}
	other := put(t, s, "m8", secondsAgo(3600, 0), "W", watts())

	var logs bytes.Buffer
	r := defaults()
	r.Readings = s
	r.Log = slog.New(slog.NewTextHandler(&logs, nil))
	removed, err := r.Sweep(context.Background(), sweepNow)
	if err != nil || removed != 1 {
		t.Fatalf("Sweep = %d, %v, want 1 removed", removed, err)
	}
	got := stored(t, s, "m7")
	if len(got) != 20000 || got[0].Href != "/mup/m7/mr/"+ids[1] || got[len(got)-1].Href != "/mup/m7/mr/"+ids[n-1] {
		t.Fatalf("stored %d readings from %s, want 20000 from %s", len(got), got[0].Href, ids[1])
	}
	if o := stored(t, s, "m8"); len(o) != 1 || o[0].Href != "/mup/m8/mr/"+other {
		t.Fatalf("mirror under the cap lost a reading: %+v", o)
	}
	var naming []string
	for _, l := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		if strings.Contains(l, "mirror=m7") {
			naming = append(naming, l)
		}
	}
	if len(naming) != 1 || !strings.Contains(naming[0], "mrid=W") || !strings.Contains(naming[0], "overCap=1") || !strings.Contains(naming[0], "level=WARN") {
		t.Fatalf("log lines naming m7 = %q, want one WARN for mrid W with overCap=1; all: %s", naming, logs.String())
	}
}

// G/W/T 5: the only typed record of a series stays while a newer record of
// its mRID survives; an aged untyped one and a series with no survivor go.
func TestSweep_KeepsOnlyTypedRecordOfSurvivingSeries(t *testing.T) {
	s := memory.NewScopedStore[sep2.MirrorMeterReading]()
	typed := put(t, s, "1", secondsAgo(30*3600, 0), "W", watts())
	put(t, s, "1", secondsAgo(29*3600, 0), "W", nil)
	put(t, s, "1", secondsAgo(29*3600, 1), "V", watts())
	recent := put(t, s, "1", secondsAgo(60, 0), "W", nil)

	r := defaults()
	r.Readings = s
	if n, err := r.Sweep(context.Background(), sweepNow); err != nil || n != 2 {
		t.Fatalf("Sweep = %d, %v, want 2 removed", n, err)
	}
	got := stored(t, s, "1")
	if len(got) != 2 || got[0].Href != "/mup/1/mr/"+typed || got[0].ReadingType == nil || got[0].MRID != "W" ||
		got[1].Href != "/mup/1/mr/"+recent || got[1].ReadingType != nil {
		t.Fatalf("stored = %+v, want the typed W %s and the recent untyped W %s", got, typed, recent)
	}
}

// A record whose href does not name its mirror cannot be addressed by id, so
// it is left and the sweep goes on.
func TestSweep_LeavesUnaddressableRecord(t *testing.T) {
	s := memory.NewScopedStore[sep2.MirrorMeterReading]()
	m := sep2.MirrorMeterReading{MRID: "W", LastUpdateTime: sweepNow.Unix() - 40*3600}
	m.Href = "/mup/2/mr/x"
	if err := s.Create(context.Background(), "1", "x", m); err != nil {
		t.Fatal(err)
	}
	put(t, s, "1", secondsAgo(40*3600, 1), "W", watts())
	r := defaults()
	r.Readings = s
	if n, err := r.Sweep(context.Background(), sweepNow); err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v, want the addressable aged reading removed", n, err)
	}
	if got := stored(t, s, "1"); len(got) != 1 || got[0].Href != "/mup/2/mr/x" {
		t.Fatalf("stored = %+v, want only the unaddressable record", got)
	}
}

func TestSweep_RefusesIncompleteRetention(t *testing.T) {
	s := memory.NewScopedStore[sep2.MirrorMeterReading]()
	mups := memory.NewStore[sep2.MirrorUsagePoint]()
	for _, r := range []*mirrorretention.Retention{
		{Mirrors: mups, MaxAge: time.Hour, MaxPerSeries: 1},
		{Readings: s, MaxAge: time.Hour, MaxPerSeries: 1},
		{Readings: s, Mirrors: mups, MaxPerSeries: 1},
		{Readings: s, Mirrors: mups, MaxAge: time.Hour},
	} {
		if _, err := r.Sweep(context.Background(), sweepNow); err != mirrorretention.ErrIncomplete {
			t.Errorf("Sweep(%+v) err = %v, want ErrIncomplete", r, err)
		}
	}
}

func TestStart_SweepsEachTickAndStops(t *testing.T) {
	s := memory.NewScopedStore[sep2.MirrorMeterReading]()
	put(t, s, "1", secondsAgo(40*3600, 0), "W", watts())
	r := defaults()
	r.Readings = s
	stop := r.Start(context.Background(), time.Millisecond, func() time.Time { return sweepNow })
	deadline := time.Now().Add(5 * time.Second)
	for len(stored(t, s, "1")) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the ticker never removed the aged reading")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	stop()
}

// Fix round item 1: the cap counts each series on its own, so a mirror with
// several series loses only the readings of the one over the cap.
func TestSweep_CapIsPerSeries(t *testing.T) {
	s := memory.NewScopedStore[sep2.MirrorMeterReading]()
	a := []string{
		put(t, s, "1", secondsAgo(600, 0), "A", watts()),
		put(t, s, "1", secondsAgo(500, 0), "A", watts()),
		put(t, s, "1", secondsAgo(400, 0), "A", watts()),
	}
	b := []string{
		put(t, s, "1", secondsAgo(700, 0), "B", watts()),
		put(t, s, "1", secondsAgo(300, 0), "B", watts()),
	}
	r := defaults()
	r.Readings = s
	r.MaxPerSeries = 2
	if n, err := r.Sweep(context.Background(), sweepNow); err != nil || n != 1 {
		t.Fatalf("Sweep = %d, %v, want 1 removed", n, err)
	}
	var got []string
	for _, m := range stored(t, s, "1") {
		got = append(got, m.Href)
	}
	want := []string{"/mup/1/mr/" + b[0], "/mup/1/mr/" + a[1], "/mup/1/mr/" + a[2], "/mup/1/mr/" + b[1]}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("stored = %v, want %v", got, want)
	}
}

func inlineMirror(t *testing.T, id string, inline ...sep2.MirrorMeterReading) *memory.Store[sep2.MirrorUsagePoint] {
	t.Helper()
	mups := memory.NewStore[sep2.MirrorUsagePoint]()
	mup := sep2.MirrorUsagePoint{MRID: "MUP" + id, MirrorMeterReading: inline}
	mup.Href = "/mup/" + id
	if err := mups.Create(context.Background(), id, mup); err != nil {
		t.Fatal(err)
	}
	return mups
}

// Fix round item 4: an inline untyped reading keeps the aged out-of-band
// record its type comes from; an inline typed one does not need it.
func TestSweep_InlineReadingsInTypeRule(t *testing.T) {
	for _, tc := range []struct {
		name     string
		inlineRT *sep2.ReadingType
		kept     int
	}{
		{"untyped inline relies on the aged record", nil, 1},
		{"typed inline needs nothing", watts(), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := memory.NewScopedStore[sep2.MirrorMeterReading]()
			typed := put(t, s, "1", secondsAgo(30*3600, 0), "W", watts())
			r := defaults()
			r.Readings = s
			r.Mirrors = inlineMirror(t, "1", sep2.MirrorMeterReading{MRID: "W", ReadingType: tc.inlineRT})
			if _, err := r.Sweep(context.Background(), sweepNow); err != nil {
				t.Fatal(err)
			}
			got := stored(t, s, "1")
			if len(got) != tc.kept || (tc.kept == 1 && (got[0].Href != "/mup/1/mr/"+typed || got[0].ReadingType == nil)) {
				t.Fatalf("stored = %+v, want %d record(s), the typed %s", got, tc.kept, typed)
			}
		})
	}
}

type failingMirrors struct{}

func (failingMirrors) Get(context.Context, string) (sep2.MirrorUsagePoint, error) {
	return sep2.MirrorUsagePoint{}, errors.New("mirror store down")
}

// A mirror whose inline readings cannot be read keeps every record this sweep.
func TestSweep_MirrorReadFailureRemovesNothing(t *testing.T) {
	s := memory.NewScopedStore[sep2.MirrorMeterReading]()
	put(t, s, "1", secondsAgo(40*3600, 0), "W", watts())
	r := defaults()
	r.Readings = s
	r.Mirrors = failingMirrors{}
	if n, err := r.Sweep(context.Background(), sweepNow); err != nil || n != 0 || len(stored(t, s, "1")) != 1 {
		t.Fatalf("Sweep = %d, %v, want nothing removed", n, err)
	}
}
