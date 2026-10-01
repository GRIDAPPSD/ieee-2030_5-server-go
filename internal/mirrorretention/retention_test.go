package mirrorretention_test

import (
	"bytes"
	"context"
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
		MaxAge:       config.DefaultMirrorReadingRetention,
		MaxPerMirror: config.DefaultMirrorReadingMaxPerMirror,
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

// G/W/T 2: over the cap the oldest goes, and one log line names the mirror.
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
	if len(naming) != 1 || !strings.Contains(naming[0], "overCap=1") || !strings.Contains(naming[0], "level=WARN") {
		t.Fatalf("log lines naming m7 = %q, want one WARN with overCap=1; all: %s", naming, logs.String())
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
	for _, r := range []*mirrorretention.Retention{
		{MaxAge: time.Hour, MaxPerMirror: 1},
		{Readings: s, MaxPerMirror: 1},
		{Readings: s, MaxAge: time.Hour},
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
