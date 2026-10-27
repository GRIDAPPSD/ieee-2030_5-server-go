package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

const (
	historyHour = int64(3600)
	// historyFinal is the end of the newest bucket that is final at powerNow.
	historyFinal = powerNow - maxHoldSeconds
)

// readings is one W reading per 50 s from start up to end, each holding for
// its post rate, so the device covers every second between them.
func readings(start, end int64, watts func(i int) int64) [][2]int64 {
	var out [][2]int64
	for t, i := start, 0; t < end; t, i = t+50, i+1 {
		out = append(out, [2]int64{t, watts(i)})
	}
	return out
}

func constWatts(w int64) func(int) int64 { return func(int) int64 { return w } }

// withHistory is the harness's handler with hist attached.
func (p *powerHarness) withHistory(hist *PowerHistory) *AdminFleetHandler {
	return &AdminFleetHandler{Managers: p.mgr, MirrorUsagePoints: p.mups, MirrorMeterReadings: p.mmrs, PowerHistory: hist}
}

type historyAnswer struct {
	powerAnswer
	EnergyChart struct {
		Series []powerChartSeries
	}
}

func (a historyAnswer) energy(name string) *powerChartSeries {
	for i := range a.EnergyChart.Series {
		if a.EnergyChart.Series[i].Name == name {
			return &a.EnergyChart.Series[i]
		}
	}
	return nil
}

func routeAnswer(t *testing.T, h *AdminFleetHandler, query string) historyAnswer {
	t.Helper()
	w := httptest.NewRecorder()
	HandlePower(h, 0)(w, httptest.NewRequest(http.MethodGet, "/api/derms/power?"+query, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var a historyAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func openHistory(t *testing.T, dir string, retention time.Duration, now int64) *PowerHistory {
	t.Helper()
	hist, err := OpenPowerHistory(dir, retention, now)
	if err != nil {
		t.Fatal(err)
	}
	return hist
}

// twoDeviceHour posts an hour of readings ending at powerNow: D1 exports
// 2000 W plus a step that varies by reading, D2 imports 1000 W.
func twoDeviceHour(p *powerHarness) {
	p.manage(powerD1, powerD2)
	const rev, fwd = sep2.FlowDirectionReverse, sep2.FlowDirectionForward
	p.post(powerD1, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow, func(i int) int64 { return 2000 + 100*int64(i%3) })...)
	p.post(powerD2, roleFlagIsDER, 50, fwd, readings(powerNow-historyHour, powerNow, constWatts(1000))...)
}

func readFileRecords(t *testing.T, path string) []historyRecord {
	t.Helper()
	recs, dropped, err := readHistoryFile(path)
	if err != nil || dropped != 0 {
		t.Fatalf("readHistoryFile(%s) = %d records, %d dropped, %v; want a clean file", path, len(recs), dropped, err)
	}
	return recs
}

func seriesAt(s *powerChartSeries, at int64) (float64, bool) {
	for _, pt := range s.Points {
		if int64(pt[0])/1000 == at {
			return pt[1], true
		}
	}
	return 0, false
}

// A recorded bucket equals what the route computes from readings for that
// bucket, and is recorded only once it is final, 900 s after its end.
func TestPowerHistory_RecordEqualsTheRouteAndWaitsForFinal(t *testing.T) {
	p := newPowerHarness(t)
	twoDeviceHour(p)
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}

	recs := readFileRecords(t, filepath.Join(dir, "power-history-all.jsonl"))
	// 2700 s of final buckets, the first of which opens the hour.
	if want := int(historyHour-maxHoldSeconds) / 5; len(recs) != want {
		t.Fatalf("records = %d, want %d", len(recs), want)
	}
	if last := recs[len(recs)-1].At; last+5 != historyFinal {
		t.Fatalf("newest record starts at %d, want the bucket ending at %d (now - 900)", last, historyFinal)
	}

	live := routeAnswer(t, p.withHistory(nil), "scope=all&window=3600")
	total, dis, chg := live.series("Total"), live.energy("Discharged"), live.energy("Charged")
	for _, r := range recs {
		w, ok := seriesAt(total, r.At)
		if !ok {
			t.Fatalf("route has no Total point at %d", r.At)
		}
		if got := (r.ExportWs + r.ImportWs) / 5; math.Abs(got-w) > 1e-9 {
			t.Errorf("bucket %d: record Total = %v W, route = %v W", r.At, got, w)
		}
		d, _ := seriesAt(dis, r.At)
		c, _ := seriesAt(chg, r.At)
		if math.Abs(r.ExportWs/3.6e6-d) > 1e-12 || math.Abs(r.ImportWs/3.6e6-c) > 1e-12 {
			t.Errorf("bucket %d: record energy = %v and %v kWh, route = %v and %v kWh", r.At, r.ExportWs/3.6e6, r.ImportWs/3.6e6, d, c)
		}
		if r.Reporting != 2 || !r.Covered {
			t.Errorf("bucket %d: reporting %d covered %v, want 2 and true", r.At, r.Reporting, r.Covered)
		}
	}
	// D1 is 2000/2100/2200 W by reading and D2 imports 1000 W, so the first
	// bucket pins the values themselves, not only their agreement.
	if recs[0].ExportWs != 10000 || recs[0].ImportWs != -5000 {
		t.Errorf("first record = %+v, want export 10000 Ws and import -5000 Ws", recs[0])
	}

	// 900 s later the next buckets are final and appear.
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow+maxHoldSeconds); err != nil {
		t.Fatal(err)
	}
	if got := readFileRecords(t, filepath.Join(dir, "power-history-all.jsonl")); len(got) <= len(recs) {
		t.Errorf("records after 900 s = %d, want more than %d", len(got), len(recs))
	}
}

// After a restart with the same directory the 24 h energy series still has
// its earlier buckets, equal to what the readings gave before.
func TestPowerHistory_RestartKeepsEarlierBuckets(t *testing.T) {
	p := newPowerHarness(t)
	twoDeviceHour(p)
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}
	before := routeAnswer(t, p.withHistory(nil), "scope=all&window=86400")

	// A new process: no readings, the history reopened from the directory.
	restarted := newPowerHarness(t)
	restarted.manage(powerD1, powerD2)
	after := routeAnswer(t, restarted.withHistory(openHistory(t, dir, 48*time.Hour, powerNow)), "scope=all&window=86400")

	if after.BucketSeconds != 120 {
		t.Fatalf("bucketSeconds = %d, want 120", after.BucketSeconds)
	}
	total, dis, chg := after.series("Total"), after.energy("Discharged"), after.energy("Charged")
	if total == nil || dis == nil || chg == nil {
		t.Fatalf("restarted answer lost its series: %+v", after)
	}
	// 2700 s of final buckets is 22 whole 120 s buckets plus a part.
	var served int
	bt, bd, bc := before.series("Total"), before.energy("Discharged"), before.energy("Charged")
	for _, pt := range bt.Points {
		at := int64(pt[0]) / 1000
		if at+120 > historyFinal {
			if _, ok := seriesAt(total, at); ok {
				t.Errorf("bucket %d is not final and must not come from the history", at)
			}
			continue
		}
		served++
		for _, c := range []struct {
			name         string
			was, now     *powerChartSeries
			tolerance    float64
			wantNonEmpty bool
		}{{"Total", bt, total, 1e-9, true}, {"Discharged", bd, dis, 1e-12, true}, {"Charged", bc, chg, 1e-12, true}} {
			was, _ := seriesAt(c.was, at)
			now, ok := seriesAt(c.now, at)
			if !ok || math.Abs(was-now) > c.tolerance {
				t.Errorf("%s at %d: after restart = %v (present %v), before = %v", c.name, at, now, ok, was)
			}
		}
	}
	if served < 20 {
		t.Fatalf("only %d buckets were served from the history, want at least 20", served)
	}
	if len(after.Devices) != 0 || len(after.Chart.Series) != 1 {
		t.Errorf("devices = %d, series = %d; after a restart the per-device lines are absent and only the Total is drawn", len(after.Devices), len(after.Chart.Series))
	}
}

// A device that reported in the hour but not in a bucket makes that bucket a
// gap; the recorded gaps still count after the readings are gone.
func TestPowerHistory_GapsSurviveARestart(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	const rev = sep2.FlowDirectionReverse
	p.post(powerD1, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow, constWatts(1000))...)
	p.post(powerD2, roleFlagIsDER, 50, rev, readings(powerNow-3000, powerNow-2950, constWatts(500))...)
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}

	recs := readFileRecords(t, filepath.Join(dir, "power-history-all.jsonl"))
	var gaps, full, solo int
	for _, r := range recs {
		switch {
		case r.Covered && r.Reporting == 1:
			solo++
			if r.ExportWs != 1000*5 {
				t.Errorf("solo bucket %d exports %v Ws, want 5000", r.At, r.ExportWs)
			}
		case r.Covered:
			full++
			if r.ExportWs != 1500*5 {
				t.Errorf("full bucket %d exports %v Ws, want 7500", r.At, r.ExportWs)
			}
		default:
			gaps++
			if r.ExportWs != 0 || r.ImportWs != 0 {
				t.Errorf("gap bucket %d carries totals %+v", r.At, r)
			}
		}
	}
	// D2 is heard from now-3000 to now-2950 and then reports for an hour.
	// Before it is heard D1 is alone (120 buckets), then 10 buckets both
	// cover, then D1 alone covers while D2 is silent: 410 gaps to the final end.
	if solo != 120 || full != 10 || gaps != 410 {
		t.Fatalf("solo = %d, full = %d, gaps = %d, want 120, 10 and 410", solo, full, gaps)
	}

	restarted := newPowerHarness(t)
	restarted.manage(powerD1, powerD2)
	after := routeAnswer(t, restarted.withHistory(openHistory(t, dir, 48*time.Hour, powerNow)), "scope=all&window=3600")
	if after.TotalGapBuckets != 410 {
		t.Errorf("totalGapBuckets after restart = %d, want 410", after.TotalGapBuckets)
	}
	if pts := after.series("Total").Points; len(pts) != 130 {
		t.Errorf("Total points after restart = %d, want the 130 covered buckets", len(pts))
	}
}

// A fleet scope is recorded under the aggregator, apart from scope all.
func TestPowerHistory_FleetScopeIsSeparate(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1)
	const rev = sep2.FlowDirectionReverse
	p.post(powerD1, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow, constWatts(1000))...)
	p.post(powerD2, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow, constWatts(3000))...)
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}
	all := readFileRecords(t, filepath.Join(dir, "power-history-all.jsonl"))
	fleet := readFileRecords(t, filepath.Join(dir, "power-history-"+powerAgg+".jsonl"))
	// Scope all holds D1 and D2 (4000 W); the aggregator's fleet is D1 and
	// the aggregator itself, which posts nothing (1000 W).
	if all[0].ExportWs != 20000 || fleet[0].ExportWs != 5000 || all[0].Reporting != 2 || fleet[0].Reporting != 1 {
		t.Errorf("first buckets: all = %+v, fleet = %+v; want 20000 Ws over 2 devices and 5000 Ws over 1", all[0], fleet[0])
	}
}

// The history is bounded: a record older than the retention is removed from
// the file at the next prune, and the file keeps every newer one.
func TestPowerHistory_PruneKeepsTheFileWithinTheBound(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionReverse, readings(powerNow-3*historyHour, powerNow, constWatts(1000))...)
	dir := t.TempDir()
	path := filepath.Join(dir, "power-history-all.jsonl")
	retention := 2 * time.Hour
	hist := openHistory(t, dir, retention, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}
	first := readFileRecords(t, path)
	if want := int(2 * historyHour / 5); len(first) != want {
		t.Fatalf("records = %d, want %d: only the retention is recorded", len(first), want)
	}

	later := powerNow + historyHour + 5
	if err := hist.Record(context.Background(), p.withHistory(hist), later); err != nil {
		t.Fatal(err)
	}
	got := readFileRecords(t, path)
	cutoff := later - 2*historyHour
	if got[0].At < cutoff {
		t.Errorf("oldest record %d is older than the bound %d", got[0].At, cutoff)
	}
	// The newer first-pass records are all kept, and every bucket from
	// historyFinal to the new final end is recorded: covered while the
	// readings last, then as a gap while the device is still reporting.
	kept := len(first) - slices.IndexFunc(first, func(r historyRecord) bool { return r.At >= cutoff })
	if want := kept + int((finalEnd(later)-historyFinal)/5); len(got) != want {
		t.Errorf("records after prune = %d, want %d (%d kept, the rest new)", len(got), want, kept)
	}
	if _, state := hist.lookup(powerHistoryAllScope, first[0].At, first[0].At+5); state != historyNone {
		t.Errorf("the pruned bucket is still served from memory: state %v", state)
	}

	// A reopen past the bound drops what has aged out and empties the file.
	if _, err := OpenPowerHistory(dir, retention, later+3*historyHour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a history with no record in the bound leaves its file: %v", err)
	}
}

func mkRecord(at int64, x, i float64, n int, covered bool) historyRecord {
	r := historyRecord{At: at, ExportWs: x, ImportWs: i, Reporting: n, Covered: covered}
	r.Sum = r.checksum()
	return r
}

// A damaged file is not served: torn, altered, out of order and invalid
// lines are dropped, the good ones are kept, and the file is rewritten clean.
func TestPowerHistory_DamagedLinesAreDroppedAndTheFileRewritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "power-history-all.jsonl")
	good1, good2 := mkRecord(1000, 100, -20, 2, true), mkRecord(1010, 300, 0, 2, true)
	altered := mkRecord(1005, 100, 0, 1, true)
	altered.ExportWs = 999 // checksum no longer matches
	lines := []string{
		mustJSON(t, good1),
		mustJSON(t, altered),
		"{not json",
		mustJSON(t, mkRecord(1000, 1, 0, 1, true)),  // not after good1
		mustJSON(t, mkRecord(1007, 1, 0, 1, true)),  // not a multiple of 5
		mustJSON(t, mkRecord(1015, -4, 0, 1, true)), // negative export
		mustJSON(t, good2),
		`{"t":1020,"x":5,"i":0,"n":1,"c":tr`, // torn last line
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	hist := openHistory(t, dir, 48*time.Hour, 1100)
	if got := readFileRecords(t, path); !slices.Equal(got, []historyRecord{good1, good2}) {
		t.Fatalf("file after open = %+v, want only the two good records", got)
	}
	// The altered record is gone, so its part counts as uncovered and the
	// bucket is drawn from the good part alone, scaled by 2 parts over 1.
	if tot, state := hist.lookup(powerHistoryAllScope, 1000, 1010); state != historyCovered || tot.exportWs != 200 || tot.importWs != -40 {
		t.Errorf("bucket over a dropped record = %+v %v, want covered with 200 and -40 Ws", tot, state)
	}
	if tot, state := hist.lookup(powerHistoryAllScope, 1010, 1015); state != historyCovered || tot.exportWs != 300 {
		t.Errorf("good record not served: %+v %v", tot, state)
	}
}

func mustJSON(t *testing.T, r historyRecord) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A file name that is not a scope is left alone, and the retention must be
// positive.
func TestOpenPowerHistory_IgnoresForeignFilesAndRefusesNoRetention(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"power-history-../x.jsonl", "power-history-xyz.jsonl", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(name)), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	hist := openHistory(t, dir, time.Hour, 1000)
	if len(hist.scopes) != 0 {
		t.Errorf("scopes = %v, want none from foreign files", hist.scopes)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "power-history-xyz.jsonl")); string(b) != "keep" {
		t.Errorf("a foreign file was changed: %q", b)
	}
	if _, err := OpenPowerHistory(dir, 0, 1000); err == nil {
		t.Error("OpenPowerHistory with no retention succeeded")
	}
	if _, err := OpenPowerHistory("", time.Hour, 1000); err == nil {
		t.Error("OpenPowerHistory with no directory succeeded")
	}
}

// A failed write leaves the cursor where it was, so the next pass records the
// same buckets and the file ends up whole.
func TestPowerHistory_FailedWriteIsRetried(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory does not stop root")
	}
	p := newPowerHarness(t)
	p.manage(powerD1)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionReverse, readings(powerNow-historyHour, powerNow, constWatts(1000))...)
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err == nil {
		t.Fatal("Record succeeded in a read-only directory")
	}
	if tot, state := hist.lookup(powerHistoryAllScope, powerNow-historyHour, powerNow-historyHour+5); state != historyNone {
		t.Errorf("a bucket whose write failed is served: %+v %v", tot, state)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}
	recs := readFileRecords(t, filepath.Join(dir, "power-history-all.jsonl"))
	if want := int(historyHour-maxHoldSeconds) / 5; len(recs) != want {
		t.Errorf("records after the retry = %d, want %d", len(recs), want)
	}
}

// Without a history the route reads only its readings, and a history with
// nothing recorded answers the same: view is nil without one.
func TestPowerHistory_NilAndEmptyHistoryChangeNothing(t *testing.T) {
	var hist *PowerHistory
	if hist.view("all", powerNow) != nil {
		t.Error("a nil history returned a lookup")
	}
	p := newPowerHarness(t)
	twoDeviceHour(p)
	without := routeAnswer(t, p.withHistory(nil), "scope=all&window=3600")
	empty := routeAnswer(t, p.withHistory(openHistory(t, t.TempDir(), time.Hour, powerNow)), "scope=all&window=3600")
	if n := len(without.series("Total").Points); n != 720 {
		t.Fatalf("Total points without a history = %d, want 720", n)
	}
	same := func(a, b []powerChartSeries) bool {
		return slices.EqualFunc(a, b, func(x, y powerChartSeries) bool { return x.Name == y.Name && slices.Equal(x.Points, y.Points) })
	}
	if !same(without.Chart.Series, empty.Chart.Series) || !same(without.EnergyChart.Series, empty.EnergyChart.Series) || without.TotalGapBuckets != empty.TotalGapBuckets {
		t.Error("an empty history changed the answer")
	}
}

// The recorder loop records on its own and stops when told.
func TestPowerHistory_StartRecordsAndStops(t *testing.T) {
	p := newPowerHarness(t)
	twoDeviceHour(p)
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	stop := hist.Start(context.Background(), p.withHistory(hist))
	deadline := time.After(PowerHistoryInterval + 5*time.Second)
	path := filepath.Join(dir, "power-history-all.jsonl")
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		select {
		case <-deadline:
			stop()
			t.Fatal("the recorder wrote nothing within one interval")
		case <-time.After(100 * time.Millisecond):
		}
	}
	stop()
	if got := readFileRecords(t, path); len(got) == 0 {
		t.Error("recorder file is empty")
	}
}

// boundsFor steps by its step argument and includes both ends.
func TestBoundsFor_StepsByStepAndIncludesBothEnds(t *testing.T) {
	for _, c := range []struct {
		start, end, step int64
		want             []int64
	}{
		{0, 20, 5, []int64{0, 5, 10, 15, 20}},
		{10, 40, 15, []int64{10, 25, 40}},
		{100, 100, 30, []int64{100}},
		{100, 119, 30, []int64{100}},
	} {
		if got := boundsFor(c.start, c.end, c.step); !slices.Equal(got, c.want) {
			t.Errorf("boundsFor(%d, %d, %d) = %v, want %v", c.start, c.end, c.step, got, c.want)
		}
	}
}

// The latest grid steps 5 s over the last 900 s and ends on the last closed
// 5 s boundary.
func TestLatestGrid_FiveSecondStepsEndingOnTheClosedBoundary(t *testing.T) {
	got := latestGrid(1_700_000_403)
	if len(got) != 181 || got[0] != 1_700_000_400-900 || got[1]-got[0] != 5 || got[180] != 1_700_000_400 {
		t.Errorf("latestGrid = %d points from %d (step %d) to %d, want 181 from %d by 5 to %d",
			len(got), got[0], got[1]-got[0], got[len(got)-1], 1_700_000_400-900, 1_700_000_400)
	}
}

// Which devices are reporting does not depend on the chart window or on how
// many buckets one recording pass covers: device 2 was last heard 20 h ago,
// so it is not reporting during the 3 h device 1 posts, and the record-served
// answer equals the one computed from readings.
func TestPowerHistory_ReportingRuleIsTheSameInTheRouteAndTheRecord(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	const rev = sep2.FlowDirectionReverse
	p.post(powerD1, roleFlagIsDER, 50, rev, readings(powerNow-3*historyHour, powerNow, constWatts(2000))...)
	p.post(powerD2, roleFlagIsDER, 50, rev, readings(powerNow-20*historyHour, powerNow-20*historyHour+50, constWatts(500))...)
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}

	live := routeAnswer(t, p.withHistory(nil), "scope=all&window=86400")
	restarted := newPowerHarness(t)
	restarted.manage(powerD1, powerD2)
	served := routeAnswer(t, restarted.withHistory(openHistory(t, dir, 48*time.Hour, powerNow)), "scope=all&window=86400")

	// Device 2 reports for the hour after it was heard and is silent in it
	// (30 buckets of 120 s); while device 1 posts it is not reporting.
	if live.TotalGapBuckets != 30 {
		t.Errorf("readings give %d gap buckets, want 30: only the hour after device 2 was last heard", live.TotalGapBuckets)
	}
	if served.TotalGapBuckets != live.TotalGapBuckets {
		t.Errorf("record-served gaps = %d, readings = %d", served.TotalGapBuckets, live.TotalGapBuckets)
	}
	lt, st := live.series("Total"), served.series("Total")
	if lt == nil || st == nil {
		t.Fatalf("Total missing: live %v served %v", lt != nil, st != nil)
	}
	var final int
	for _, pt := range lt.Points {
		at := int64(pt[0]) / 1000
		if at+120 > historyFinal {
			continue
		}
		final++
		if got, ok := seriesAt(st, at); !ok || got != pt[1] {
			t.Errorf("bucket %d: record-served = %v (present %v), readings = %v", at, got, ok, pt[1])
		}
	}
	if final < 80 {
		t.Errorf("only %d final Total points from readings, want at least 80 over the 3 h", final)
	}
	if got, ok := seriesAt(lt, powerNow-2*historyHour); !ok || got != 2000 {
		t.Errorf("Total two hours ago = %v (present %v), want 2000", got, ok)
	}
}

// A 30 s bucket is built from the same 5 s totals the recorder stores, so a
// device with a short gap in each 5 s part gives the same answer from
// readings and from the record.
func TestPowerHistory_CoarseBucketsEqualTheRecord(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	const rev, fwd = sep2.FlowDirectionReverse, sep2.FlowDirectionForward
	p.post(powerD1, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow, func(i int) int64 { return 2000 + 100*int64(i%3) })...)
	// Posts every 50 s and holds 48 s: 2 s uncovered in each, so some 5 s
	// parts are covered for 3 of 5 s and coverage is unequal between devices.
	p.post(powerD2, roleFlagIsDER, 48, fwd, readings(powerNow-historyHour, powerNow, func(i int) int64 { return 1000 + 300*int64(i%4) })...)
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}
	live := routeAnswer(t, p.withHistory(nil), "scope=all&window=21600")
	restarted := newPowerHarness(t)
	restarted.manage(powerD1, powerD2)
	served := routeAnswer(t, restarted.withHistory(openHistory(t, dir, 48*time.Hour, powerNow)), "scope=all&window=21600")
	if live.BucketSeconds != 30 {
		t.Fatalf("bucketSeconds = %d, want 30", live.BucketSeconds)
	}
	compared := 0
	for _, c := range []struct {
		name      string
		live, rec *powerChartSeries
	}{
		{"Total", live.series("Total"), served.series("Total")},
		{"Discharged", live.energy("Discharged"), served.energy("Discharged")},
		{"Charged", live.energy("Charged"), served.energy("Charged")},
	} {
		if c.live == nil || c.rec == nil {
			t.Fatalf("%s missing", c.name)
		}
		for _, pt := range c.live.Points {
			at := int64(pt[0]) / 1000
			if at+30 > historyFinal {
				continue
			}
			compared++
			if got, ok := seriesAt(c.rec, at); !ok || math.Abs(got-pt[1]) > 1e-9 {
				t.Errorf("%s at %d: record-served = %v (present %v), readings = %v", c.name, at, got, ok, pt[1])
			}
		}
	}
	if compared < 250 {
		t.Errorf("compared %d points, want at least 250", compared)
	}
}

// The newest final bucket ends on a 5 s boundary however the clock falls, so
// every record is on the grid and a reload keeps all of them.
func TestPowerHistory_FinalEndIsFlooredToTheGrid(t *testing.T) {
	if got, want := finalEnd(powerNow+3), powerNow-maxHoldSeconds; got != want {
		t.Errorf("finalEnd(now+3) = %d, want %d", got, want)
	}
	p := newPowerHarness(t)
	twoDeviceHour(p)
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow+3); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "power-history-all.jsonl")
	recs := readFileRecords(t, path)
	for _, r := range recs {
		if r.At%5 != 0 {
			t.Fatalf("record at %d is off the 5 s grid", r.At)
		}
	}
	if len(recs) == 0 || recs[len(recs)-1].At+5 != historyFinal {
		t.Fatalf("records = %d, newest ends at %d, want %d", len(recs), recs[len(recs)-1].At+5, historyFinal)
	}
	reopened := openHistory(t, dir, 48*time.Hour, powerNow+3)
	if n := len(reopened.scopes[powerHistoryAllScope].recs); n != len(recs) {
		t.Errorf("records after reopen = %d, want %d", n, len(recs))
	}
}

// An append starts on a clean line: a file whose last line lost its newline
// does not glue the next record onto it.
func TestPowerHistory_AppendAfterALineWithoutNewline(t *testing.T) {
	p := newPowerHarness(t)
	twoDeviceHour(p)
	dir := t.TempDir()
	path := filepath.Join(dir, "power-history-all.jsonl")
	old := mkRecord(historyFinal-7200, 100, -20, 2, true)
	if err := os.WriteFile(path, []byte(mustJSON(t, old)), 0o600); err != nil {
		t.Fatal(err)
	}
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}
	recs := readFileRecords(t, path)
	if len(recs) < 2 || recs[0] != old {
		t.Fatalf("records = %d, first %+v, want the old record kept and new ones after it", len(recs), recs[0])
	}
	if data, _ := os.ReadFile(path); len(data) == 0 || data[len(data)-1] != '\n' {
		t.Error("file does not end with a newline")
	}
}

// A device posting 300 s Averages 5 s late each time leaves a wholly
// uncovered 5 s part in some buckets; those buckets are still drawn, from the
// parts that are covered, and a record serves the same points.
func TestPowerHistory_LateAverageDeviceDrawsEveryBucket(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1)
	// An Average covers the 300 s before its receipt, so a 305 s cadence
	// leaves 5 s uncovered after each one.
	postRate, uom, dq, flow := uint32(300), sep2.UomWatts, dataQualifierAverage, sep2.FlowDirectionReverse
	mup := sep2.MirrorUsagePoint{MRID: "MUPA", DeviceLFDI: powerD1, PostRate: &postRate, RoleFlags: roleFlagIsDER}
	mup.Href = "/mup/9"
	if err := p.mups.Create(context.Background(), "9", mup); err != nil {
		t.Fatal(err)
	}
	for i, at := 0, powerNow-6*historyHour; at < powerNow; i, at = i+1, at+305 {
		v := int64(1000)
		r := sep2.MirrorMeterReading{
			MRID:           "WA",
			LastUpdateTime: at,
			Reading:        &sep2.Reading{Value: &v},
			ReadingType:    &sep2.ReadingType{Uom: &uom, DataQualifier: &dq, FlowDirection: &flow},
		}
		if err := p.mmrs.Create(context.Background(), "9", fmt.Sprint(i), r); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}
	live := routeAnswer(t, p.withHistory(nil), "scope=all&window=86400")
	total := live.series("Total")
	inWindow := 0
	for _, pt := range total.Points {
		at := int64(pt[0]) / 1000
		if at >= powerNow-6*historyHour+305 && at+120 <= historyFinal {
			inWindow++
			if pt[1] != 1000 {
				t.Errorf("bucket %d = %v W, want 1000", at, pt[1])
			}
		}
	}
	if want := int((6*historyHour - 305 - maxHoldSeconds) / 120); inWindow < want-1 {
		t.Errorf("final Total points = %d, want about %d: buckets with an uncovered 5 s part must still draw", inWindow, want)
	}
	restarted := newPowerHarness(t)
	restarted.manage(powerD1)
	served := routeAnswer(t, restarted.withHistory(openHistory(t, dir, 48*time.Hour, powerNow)), "scope=all&window=86400")
	st := served.series("Total")
	for _, pt := range total.Points {
		if at := int64(pt[0]) / 1000; at+120 <= historyFinal {
			if got, ok := seriesAt(st, at); !ok || math.Abs(got-pt[1]) > 1e-9 {
				t.Errorf("bucket %d: record-served = %v (present %v), readings = %v", at, got, ok, pt[1])
			}
		}
	}
}

// The history answers for a bucket only when it ends at or before the final
// end: the bucket ending on it is served and the next one is not, though a
// later pass recorded it.
func TestPowerHistory_ViewBoundaryIsTheFinalEnd(t *testing.T) {
	p := newPowerHarness(t)
	twoDeviceHour(p)
	hist := openHistory(t, t.TempDir(), 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow+maxHoldSeconds); err != nil {
		t.Fatal(err)
	}
	view := hist.view(powerHistoryAllScope, powerNow)
	if _, state := view(historyFinal-5, historyFinal); state != historyCovered {
		t.Errorf("bucket ending at the final end: state %v, want covered", state)
	}
	if _, state := hist.lookup(powerHistoryAllScope, historyFinal, historyFinal+5); state != historyCovered {
		t.Fatalf("setup: the bucket after the final end was not recorded: state %v", state)
	}
	if _, state := view(historyFinal, historyFinal+5); state != historyNone {
		t.Errorf("bucket ending 5 s past the final end: state %v, want none", state)
	}
}
