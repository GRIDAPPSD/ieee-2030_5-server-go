package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	twinAgg2 = "AAAA000000000000000000000000000000000002"
	twinD3   = "BBBB000000000000000000000000000000000003"
	twinD4   = "BBBB000000000000000000000000000000000004"
	twinD5   = "BBBB000000000000000000000000000000000005"

	// twinRestart is S, a multiple of every bucket size.
	twinRestart = powerNow
	twinStart   = twinRestart - 2*historyHour
)

// twinDevice posts one kind of W reading every period seconds.
type twinDevice struct {
	lfdi     string
	mup      string
	postRate uint32
	period   int64
	quietAt  int64   // no reading at or after this time; 0 is never quiet
	at       []int64 // when set, the only times the device posts
	reading  func(at int64, i int) sep2.MirrorMeterReading
}

func twinW(mrid string, at int64, watts int64, rt *sep2.ReadingType) sep2.MirrorMeterReading {
	return sep2.MirrorMeterReading{MRID: mrid, LastUpdateTime: at, Reading: &sep2.Reading{Value: &watts}, ReadingType: rt}
}

func twinType(dir uint8, average bool) *sep2.ReadingType {
	uom := sep2.UomWatts
	rt := &sep2.ReadingType{Uom: &uom, FlowDirection: &dir}
	if average {
		q := dataQualifierAverage
		rt.DataQualifier = &q
	}
	return rt
}

// twinDevices are three devices in two aggregators and one Direct device:
// an instantaneous one, an Average one with no timePeriod whose mRID carries
// its ReadingType on the first reading only (2023 rule (n)), an Average one
// with a timePeriod, and an instantaneous Direct one that goes quiet when
// quietD4 is set.
func twinDevices(quietD4 bool) []twinDevice {
	const rev, fwd = sep2.FlowDirectionReverse, sep2.FlowDirectionForward
	d4 := twinDevice{lfdi: twinD4, mup: "4", postRate: 900, period: 100, reading: func(at int64, i int) sep2.MirrorMeterReading {
		return twinW("D4W", at, 300+int64(i%2)*20, twinType(rev, false))
	}}
	if quietD4 {
		d4.quietAt = twinRestart - 2000
	}
	return []twinDevice{
		{lfdi: powerD1, mup: "1", postRate: 50, period: 50, reading: func(at int64, i int) sep2.MirrorMeterReading {
			return twinW("D1W", at, 2000+100*int64(i%3), twinType(rev, false))
		}},
		{lfdi: powerD2, mup: "2", postRate: 60, period: 60, reading: func(at int64, i int) sep2.MirrorMeterReading {
			var rt *sep2.ReadingType
			if i == 0 {
				rt = twinType(fwd, true)
			}
			return twinW("D2W", at, 1000+10*int64(i%5), rt)
		}},
		{lfdi: twinD3, mup: "3", postRate: 300, period: 300, reading: func(at int64, _ int) sep2.MirrorMeterReading {
			r := twinW("D3W", at, 500, twinType(rev, true))
			r.Reading.TimePeriod = &sep2.DateTimeInterval{Start: at - 300, Duration: 300}
			return r
		}},
		d4,
		// An instantaneous reading whose hold ends 10 s after the reporting
		// lead of the oldest bucket a restart at S leaves unrecorded, so it
		// alone decides that D5 is reporting there, and a newer one.
		{lfdi: twinD5, mup: "5", postRate: 900, at: []int64{twinRestart - maxHoldSeconds - latestReportedSeconds - maxHoldSeconds + 10, twinRestart - 100}, reading: func(at int64, _ int) sep2.MirrorMeterReading {
			return twinW("D5W", at, 700, twinType(rev, false))
		}},
	}
}

// twinSide is one process: its management, mirrors, readings and history.
type twinSide struct {
	t    *testing.T
	dir  string
	mgr  *memory.EndDeviceManagementStore
	mups *memory.Store[sep2.MirrorUsagePoint]
	mmrs store.ScopedStore[sep2.MirrorMeterReading]
	hist *PowerHistory
}

func (s *twinSide) handler() *AdminFleetHandler {
	return &AdminFleetHandler{Managers: s.mgr, MirrorUsagePoints: s.mups, MirrorMeterReadings: s.mmrs, PowerHistory: s.hist}
}

// twinOpenReadings is how a side's reading store is built at boot over its
// directory. The journal under test is swapped for a plain in-memory store
// by the control.
type twinOpenReadings func(t *testing.T, side *twinSide, now int64) store.ScopedStore[sep2.MirrorMeterReading]

func plainReadings(*testing.T, *twinSide, int64) store.ScopedStore[sep2.MirrorMeterReading] {
	return memory.NewScopedStore[sep2.MirrorMeterReading]()
}

func newTwinSide(t *testing.T, devices []twinDevice) *twinSide {
	t.Helper()
	s := &twinSide{t: t, dir: t.TempDir(), mgr: memory.NewEndDeviceManagementStore(), mups: memory.NewStore[sep2.MirrorUsagePoint]()}
	ctx := context.Background()
	for _, a := range [][2]string{{powerAgg, powerD1}, {powerAgg, powerD2}, {twinAgg2, twinD3}} {
		if err := s.mgr.Assign(ctx, a[0], a[1]); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range devices {
		rate := d.postRate
		mup := sep2.MirrorUsagePoint{MRID: "MUP" + d.mup, DeviceLFDI: d.lfdi, PostRate: &rate, RoleFlags: roleFlagIsDER}
		mup.Href = "/mup/" + d.mup
		if err := s.mups.Create(ctx, d.mup, mup); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// boot builds the side's readings and reopens its history at now.
func (s *twinSide) boot(open twinOpenReadings, now int64) {
	s.t.Helper()
	s.mmrs = open(s.t, s, now)
	s.hist = openHistory(s.t, filepath.Join(s.dir, "power-history"), 48*time.Hour, now)
	if js, ok := s.mmrs.(ReadingJournalStore); ok {
		j := &ReadingJournal{Readings: js, Mirrors: s.mups}
		if err := j.DropOrphans(context.Background()); err != nil {
			s.t.Fatal(err)
		}
		s.hist.UseJournal(j)
	}
}

// restart is a new process over the same directory: management and mirrors
// persist, the readings are what open rebuilds, and the history is reopened.
func (s *twinSide) restart(open twinOpenReadings, now int64) {
	s.t.Helper()
	s.boot(open, now)
}

type twinRun struct {
	t       *testing.T
	devices []twinDevice
	posted  map[string]int
	next    int64 // the first tick run has not stepped
}

// post stores, on every running side, each reading due at t, under one id.
func (r *twinRun) post(t int64, sides ...*twinSide) {
	r.t.Helper()
	for _, d := range r.devices {
		switch {
		case d.at != nil && !slices.Contains(d.at, t),
			d.at == nil && (t-twinStart)%d.period != 0,
			d.quietAt != 0 && t >= d.quietAt:
			continue
		}
		i := r.posted[d.lfdi]
		r.posted[d.lfdi]++
		m := d.reading(t, i)
		id := fmt.Sprintf("%019d", t*1_000_000_000)
		m.Href = "/mup/" + d.mup + "/mr/" + id
		for _, s := range sides {
			if err := s.mmrs.Create(context.Background(), d.mup, id, m); err != nil {
				r.t.Fatal(err)
			}
		}
	}
}

func (r *twinRun) record(t int64, sides ...*twinSide) {
	r.t.Helper()
	for _, s := range sides {
		if err := s.hist.Record(context.Background(), s.handler(), t); err != nil {
			r.t.Fatal(err)
		}
	}
}

// run posts every 5 s over [from, to) on sides, and records every minute
// and on the last tick. Each side records on the same ticks, and a pass
// records whatever became final since the last one, so the cadence does not
// change what either side records.
func (r *twinRun) run(from, to int64, sides ...*twinSide) {
	r.t.Helper()
	for t := from; t < to; t += PowerHistoryStep {
		r.post(t, sides...)
		if t%60 == 0 || t+PowerHistoryStep >= to {
			r.record(t, sides...)
		}
	}
	r.next = to
}

func twinPower(t *testing.T, h *AdminFleetHandler, now int64, query string) []byte {
	t.Helper()
	old := nowUnix
	nowUnix = func() int64 { return now }
	defer func() { nowUnix = old }()
	w := httptest.NewRecorder()
	HandlePower(h, 0)(w, httptest.NewRequest(http.MethodGet, "/api/derms/power?"+query, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s at %d: status %d, body %s", query, now, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

var twinQueries = []string{"scope=all", "aggregatorLFDI=" + powerAgg, "aggregatorLFDI=" + twinAgg2}

// twinDiffs compares every power answer of a and b at now, and returns one
// line per differing answer. Windows up to an hour are compared byte for
// byte. Longer windows draw their device lines from readings older than any
// restart keeps, so for them the fields the history and power now answer are
// compared: the Total line, the energy chart, the groups' energy, power now
// and the gap count.
func twinDiffs(t *testing.T, a, b *twinSide, now int64) []string {
	t.Helper()
	var diffs []string
	for _, window := range powerWindows {
		for _, q := range twinQueries {
			query := fmt.Sprintf("%s&window=%d", q, window)
			ab, bb := twinPower(t, a.handler(), now, query), twinPower(t, b.handler(), now, query)
			if window <= historyHour {
				if !bytes.Equal(ab, bb) {
					diffs = append(diffs, fmt.Sprintf("%s at %d:\n  uninterrupted %s\n  restarted     %s", query, now, ab, bb))
				}
				continue
			}
			for _, field := range []string{"total", "energyChart", "aggregatorEnergy", "latestW", "latestAt", "latestMissing", "totalGapBuckets"} {
				if fa, fb := twinField(t, ab, field), twinField(t, bb, field); !bytes.Equal(fa, fb) {
					diffs = append(diffs, fmt.Sprintf("%s at %d, %s:\n  uninterrupted %s\n  restarted     %s", query, now, field, fa, fb))
				}
			}
		}
	}
	return diffs
}

// twinField is one top-level field of a power answer, or its Total series for
// "total".
func twinField(t *testing.T, body []byte, field string) []byte {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if field != "total" {
		return m[field]
	}
	var chart struct {
		Series []json.RawMessage `json:"series"`
	}
	if err := json.Unmarshal(m["chart"], &chart); err != nil {
		t.Fatal(err)
	}
	for _, s := range chart.Series {
		var named struct{ Name string }
		if err := json.Unmarshal(s, &named); err != nil {
			t.Fatal(err)
		}
		if named.Name == powerTotalName {
			return s
		}
	}
	return nil
}

// historyFiles is every history file of a side, by name.
func historyFiles(t *testing.T, s *twinSide) map[string][]byte {
	t.Helper()
	dir := filepath.Join(s.dir, "power-history")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = data
	}
	return out
}

// recordsIn is a side's records of scope starting in [from, to).
func recordsIn(t *testing.T, s *twinSide, scope string, from, to int64) []historyRecord {
	t.Helper()
	recs := readFileRecords(t, filepath.Join(s.dir, "power-history", powerHistoryFilePrefix+scope+powerHistoryFileSuffix))
	return slices.DeleteFunc(recs, func(r historyRecord) bool { return r.At < from || r.At >= to })
}

// twinRestartRun runs A uninterrupted and B with a restart just after the
// tick at S, over downtime seconds, both from twinStart, and returns them at
// S+5+downtime. B compacts its journal on the tick at S. During the downtime
// no device reaches either side, and A keeps recording.
func twinRestartRun(t *testing.T, open twinOpenReadings, quietD4 bool, downtime int64) (r *twinRun, a, b *twinSide) {
	t.Helper()
	devices := twinDevices(quietD4)
	a, b = newTwinSide(t, devices), newTwinSide(t, devices)
	a.boot(plainReadings, twinStart)
	b.boot(open, twinStart)
	r = &twinRun{t: t, devices: devices, posted: map[string]int{}}
	r.run(twinStart, twinRestart+PowerHistoryStep, a, b)
	back := twinRestart + PowerHistoryStep + downtime
	for at := twinRestart + PowerHistoryStep; at < back; at += PowerHistoryStep {
		r.record(at, a)
	}
	b.restart(open, back)
	r.next = back
	return r, a, b
}

// journalReadings is the reading store a side boots with when it journals.
func journalReadings(t *testing.T, s *twinSide, _ int64) store.ScopedStore[sep2.MirrorMeterReading] {
	t.Helper()
	js, err := memory.NewJournaledScopedStore[sep2.MirrorMeterReading](filepath.Join(s.dir, "mirrormeterreadings.jsonl"), "MirrorMeterReading")
	if err != nil {
		t.Fatal(err)
	}
	return js
}

// The buckets in [S-900, S) are final only after the restart. Recorded from
// the readings an uninterrupted run holds, they equal the uninterrupted
// records, and every power answer after the restart equals the uninterrupted
// one, through S+1800, when the history files are byte-identical.
func TestPowerRestart_AnswersEqualAnUninterruptedRun(t *testing.T) {
	for _, quiet := range []bool{false, true} {
		t.Run(fmt.Sprintf("quiet device %v", quiet), func(t *testing.T) {
			r, a, b := twinRestartRun(t, journalReadings, quiet, 0)
			for _, at := range []int64{5, 450, 900, 1800} {
				r.run(r.next, twinRestart+at+PowerHistoryStep, a, b)
				if diffs := twinDiffs(t, a, b, twinRestart+at); len(diffs) > 0 {
					t.Errorf("S+%d: %d answers differ, first:\n%s", at, len(diffs), diffs[0])
				}
			}
			for _, scope := range []string{powerHistoryAllScope, powerAgg, twinAgg2, powerDirectKey} {
				ra, rb := recordsIn(t, a, scope, twinRestart-maxHoldSeconds, twinRestart), recordsIn(t, b, scope, twinRestart-maxHoldSeconds, twinRestart)
				if len(ra) == 0 || !slices.Equal(ra, rb) {
					t.Errorf("scope %s: records in [S-900, S) differ: uninterrupted %d, restarted %d, first unequal %s", scope, len(ra), len(rb), firstUnequal(ra, rb))
				}
			}
			fa, fb := historyFiles(t, a), historyFiles(t, b)
			if len(fa) != 4 || len(fa) != len(fb) {
				t.Fatalf("history files: uninterrupted %d, restarted %d, want 4 each", len(fa), len(fb))
			}
			for name, data := range fa {
				if !bytes.Equal(data, fb[name]) {
					t.Errorf("%s differs after the restart", name)
				}
			}
		})
	}
}

func firstUnequal(a, b []historyRecord) string {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return fmt.Sprintf("%+v against %+v", a[i], b[i])
		}
	}
	return "none in the common prefix"
}

// Control: with today's in-memory readings the same run differs, in the
// answers and in the records of [S-900, S). A reading posted after the
// restart whose timePeriod reaches back into that span is then recorded as
// covered with a device missing where the uninterrupted run records a gap.
func TestPowerRestart_WithoutTheJournalTheRestartDiffers(t *testing.T) {
	r, a, b := twinRestartRun(t, plainReadings, true, 0)
	postLate(t, r.next, "3", [2]int64{twinRestart - 600, 300}, a, b)
	r.run(r.next, twinRestart+maxHoldSeconds+2*PowerHistoryStep, a, b)
	if diffs := twinDiffs(t, a, b, twinRestart+maxHoldSeconds+PowerHistoryStep); len(diffs) == 0 {
		t.Error("no answer differs without the journal: the equality test cannot fail")
	}
	ra, rb := recordsIn(t, a, powerHistoryAllScope, twinRestart-maxHoldSeconds, twinRestart), recordsIn(t, b, powerHistoryAllScope, twinRestart-maxHoldSeconds, twinRestart)
	coveredMissing := 0
	for _, x := range rb {
		i := slices.IndexFunc(ra, func(y historyRecord) bool { return y.At == x.At })
		if x.Covered && i >= 0 && !ra[i].Covered && ra[i].Reporting > x.Reporting {
			coveredMissing++
		}
	}
	if coveredMissing != 60 {
		t.Errorf("%d buckets in [S-900, S) recorded as covered with a device missing, want the 60 of the late period; uninterrupted %d records, restarted %d", coveredMissing, len(ra), len(rb))
	}
}

// postLate stores on each side one Average reading of mirror mup received at
// at whose timePeriod is period (start, duration), 9000 W.
func postLate(t *testing.T, at int64, mup string, period [2]int64, sides ...*twinSide) {
	t.Helper()
	m := twinW("D3W", at, 9000, twinType(sep2.FlowDirectionReverse, true))
	m.Reading.TimePeriod = &sep2.DateTimeInterval{Start: period[0], Duration: uint32(period[1])}
	id := fmt.Sprintf("%019d", at*1_000_000_000+period[0]%1000+1)
	m.Href = "/mup/" + mup + "/mr/" + id
	for _, s := range sides {
		if err := s.mmrs.Create(context.Background(), mup, id, m); err != nil {
			t.Fatal(err)
		}
	}
}

// A reading posted after the restart whose timePeriod reaches back into
// [S-900, S) is counted as it would be without a restart, and one whose
// period reaches a bucket already final at S leaves that bucket's record as
// it was.
func TestPowerRestart_LateReadingsAcrossTheRestart(t *testing.T) {
	r, a, b := twinRestartRun(t, journalReadings, false, 0)
	final := twinRestart - 2*maxHoldSeconds
	before := recordsIn(t, a, powerHistoryAllScope, final, final+PowerHistoryStep)
	if len(before) != 1 {
		t.Fatalf("no record of the bucket at %d before the late post", final)
	}
	postLate(t, r.next, "3", [2]int64{twinRestart - 600, 300}, a, b)
	postLate(t, r.next, "3", [2]int64{final, 300}, a, b)
	r.run(r.next, twinRestart+maxHoldSeconds+2*PowerHistoryStep, a, b)
	for _, scope := range []string{powerHistoryAllScope, twinAgg2} {
		ra := recordsIn(t, a, scope, twinRestart-600, twinRestart-300)
		if rb := recordsIn(t, b, scope, twinRestart-600, twinRestart-300); len(ra) != 60 || !slices.Equal(ra, rb) {
			t.Errorf("scope %s: the late period's records differ: uninterrupted %d, restarted %d", scope, len(ra), len(rb))
		}
	}
	if agg := recordsIn(t, a, twinAgg2, twinRestart-600, twinRestart-595); len(agg) != 1 || agg[0].ExportWs != 9000*PowerHistoryStep {
		t.Errorf("the late reading is not what the aggregator records: %+v, want %v Ws", agg, 9000*PowerHistoryStep)
	}
	for _, s := range []*twinSide{a, b} {
		if after := recordsIn(t, s, powerHistoryAllScope, final, final+PowerHistoryStep); !slices.Equal(before, after) {
			t.Errorf("the final bucket's record changed: %+v, then %+v", before, after)
		}
	}
}

// Power now after a restart: equal to the uninterrupted value after a short
// downtime, and null after one longer than latestSpanSeconds, with every
// device whose readings no longer reach now listed as missing.
func TestPowerRestart_LatestW(t *testing.T) {
	for _, tc := range []struct {
		downtime int64
		wantNull bool
	}{{60, false}, {1000, true}} {
		t.Run(fmt.Sprintf("downtime %d s", tc.downtime), func(t *testing.T) {
			_, a, b := twinRestartRun(t, journalReadings, false, tc.downtime)
			now := twinRestart + PowerHistoryStep + tc.downtime
			type latest struct {
				LatestW       *float64
				LatestMissing []string
			}
			var pa, pb latest
			for _, x := range []struct {
				s   *twinSide
				out *latest
			}{{a, &pa}, {b, &pb}} {
				if err := json.Unmarshal(twinPower(t, x.s.handler(), now, "scope=all&window=900"), x.out); err != nil {
					t.Fatal(err)
				}
			}
			if !slices.Equal(pa.LatestMissing, pb.LatestMissing) || (pa.LatestW == nil) != (pb.LatestW == nil) || (pa.LatestW != nil && *pa.LatestW != *pb.LatestW) {
				t.Fatalf("latest: uninterrupted %v %v, restarted %v %v", ptrVal(pa.LatestW), pa.LatestMissing, ptrVal(pb.LatestW), pb.LatestMissing)
			}
			if tc.wantNull {
				// D4 and D5 hold for 900 s from readings at S and S-100, so
				// they still have a value; the others are missing.
				want := []string{powerD1, powerD2, twinD3}
				if pb.LatestW != nil || !slices.Equal(pb.LatestMissing, want) {
					t.Errorf("after %d s down: latestW %v, missing %v; want null and %v", tc.downtime, ptrVal(pb.LatestW), pb.LatestMissing, want)
				}
				return
			}
			if pb.LatestW == nil || len(pb.LatestMissing) != 0 {
				t.Errorf("after %d s down: latestW %v, missing %v; want a value", tc.downtime, ptrVal(pb.LatestW), pb.LatestMissing)
			}
		})
	}
}

func ptrVal(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}
