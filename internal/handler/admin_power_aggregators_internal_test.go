package handler

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

const (
	powerAgg2 = "AAAA000000000000000000000000000000000002"
	powerD3   = "BBBB000000000000000000000000000000000003"
)

type aggEnergyPoint [2]*float64

type aggEnergyEntry struct {
	Key           string
	Name          string
	LatestW       *float64
	LatestMissing []string
	Discharged    []aggEnergyPoint
	Charged       []aggEnergyPoint
}

type aggAnswer struct {
	historyAnswer
	LatestW          *float64
	AggregatorEnergy []aggEnergyEntry
}

func (a aggAnswer) entry(t *testing.T, key string) aggEnergyEntry {
	t.Helper()
	for _, e := range a.AggregatorEnergy {
		if e.Key == key {
			return e
		}
	}
	t.Fatalf("no aggregatorEnergy entry %q in %+v", key, a.AggregatorEnergy)
	return aggEnergyEntry{}
}

func aggRoute(t *testing.T, h *AdminFleetHandler, query string) (aggAnswer, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	HandlePower(h, 0)(w, httptest.NewRequest(http.MethodGet, "/api/derms/power?"+query, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var a aggAnswer
	if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a, w.Body.Bytes()
}

// twoAggregatorsAndOneDirect posts the last 600 s for D1 (managed by the first
// aggregator, exports 2000 W), D2 (second aggregator, exports 1000 W) and D3
// (managed by none, imports 500 W).
func twoAggregatorsAndOneDirect(t *testing.T, p *powerHarness) {
	t.Helper()
	p.manage(powerD1)
	if err := p.mgr.Assign(context.Background(), powerAgg2, powerD2); err != nil {
		t.Fatal(err)
	}
	const rev, fwd = sep2.FlowDirectionReverse, sep2.FlowDirectionForward
	p.post(powerD1, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow, constWatts(2000))...)
	p.post(powerD2, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow, constWatts(1000))...)
	p.post(powerD3, roleFlagIsDER, 50, fwd, readings(powerNow-historyHour, powerNow, constWatts(500))...)
}

func near(a, b *float64) bool {
	return a != nil && b != nil && math.Abs(*a-*b) <= 1e-9
}

// Per bucket the aggregators plus Direct sum to the energy chart in both
// series, each group carries its own values, and latestW is per group while the
// total's is unchanged.
func TestPower_AggregatorEnergySumsToTheTotal(t *testing.T) {
	p := newPowerHarness(t)
	twoAggregatorsAndOneDirect(t, p)
	a, raw := aggRoute(t, p.withHistory(nil), "scope=all&window=900")

	if len(a.AggregatorEnergy) != 3 || a.AggregatorEnergy[0].Key != powerAgg || a.AggregatorEnergy[1].Key != powerAgg2 || a.AggregatorEnergy[2].Key != "direct" {
		t.Fatalf("keys = %+v, want the two aggregators sorted then direct", a.AggregatorEnergy)
	}
	if d := a.entry(t, "direct"); d.Name != "Direct" {
		t.Errorf("direct name = %q, want Direct", d.Name)
	}
	if e := a.entry(t, powerAgg); e.Name != powerAgg {
		t.Errorf("aggregator name = %q, want its LFDI", e.Name)
	}
	dis, chg := a.energy("Discharged"), a.energy("Charged")
	if dis == nil || chg == nil || len(dis.Points) != 180 {
		t.Fatalf("energy series = %+v, want 180 points each", a.EnergyChart.Series)
	}
	const perBucket = 5.0 / 3.6e6
	want := map[string][2]float64{powerAgg: {2000, 0}, powerAgg2: {1000, 0}, "direct": {0, -500}}
	for key, w := range want {
		e := a.entry(t, key)
		if len(e.Discharged) != 180 || len(e.Charged) != 180 {
			t.Fatalf("%s points = %d and %d, want 180", key, len(e.Discharged), len(e.Charged))
		}
		if d := e.Discharged[100][1]; d == nil || math.Abs(*d-w[0]*perBucket) > 1e-12 {
			t.Errorf("%s discharged = %v, want %v kWh", key, d, w[0]*perBucket)
		}
		if c := e.Charged[100][1]; c == nil || math.Abs(*c-w[1]*perBucket) > 1e-12 {
			t.Errorf("%s charged = %v, want %v kWh", key, c, w[1]*perBucket)
		}
	}
	for i := range dis.Points {
		var sd, sc float64
		for _, e := range a.AggregatorEnergy {
			if int64(*e.Discharged[i][0]) != int64(dis.Points[i][0])/1000 {
				t.Fatalf("bucket %d start %v differs from energyChart %v", i, *e.Discharged[i][0], dis.Points[i][0])
			}
			if e.Discharged[i][1] == nil || e.Charged[i][1] == nil {
				t.Fatalf("bucket %d: %s has a null where the total is covered", i, e.Key)
			}
			sd += *e.Discharged[i][1]
			sc += *e.Charged[i][1]
		}
		if math.Abs(sd-dis.Points[i][1]) > 1e-9 || math.Abs(sc-chg.Points[i][1]) > 1e-9 {
			t.Fatalf("bucket %d: groups sum to %v and %v kWh, energyChart has %v and %v", i, sd, sc, dis.Points[i][1], chg.Points[i][1])
		}
	}
	for key, w := range map[string]float64{powerAgg: 2000, powerAgg2: 1000, "direct": -500} {
		if e := a.entry(t, key); e.LatestW == nil || *e.LatestW != w || len(e.LatestMissing) != 0 {
			t.Errorf("%s latestW = %v missing %v, want %v", key, e.LatestW, e.LatestMissing, w)
		}
	}
	if a.LatestW == nil || *a.LatestW != 2500 {
		t.Errorf("total latestW = %v, want 2500", a.LatestW)
	}

	var raw2 struct {
		Entries []map[string]json.RawMessage `json:"aggregatorEnergy"`
	}
	if err := json.Unmarshal(raw, &raw2); err != nil {
		t.Fatal(err)
	}
	if len(raw2.Entries) != 3 || string(raw2.Entries[0]["discharged"][:2]) != "[[" {
		t.Errorf("entries on the wire = %s, want [[start,kWh],...] pairs", raw)
	}
}

// A device with no reading in a bucket makes the total's bucket a gap, so every
// group's bucket is null there too.
func TestPower_AggregatorEnergyIsNullWhereAGroupHasAGap(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	const rev = sep2.FlowDirectionReverse
	p.post(powerD1, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow, constWatts(2000))...)
	p.post(powerD2, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow-1200, constWatts(1000))...)
	if err := p.mgr.Assign(context.Background(), powerAgg2, powerD3); err != nil {
		t.Fatal(err)
	}
	p.post(powerD3, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow, constWatts(700))...)

	a, _ := aggRoute(t, p.withHistory(nil), "scope=all&window=3600")
	e1, e2 := a.entry(t, powerAgg), a.entry(t, powerAgg2)
	last := len(e1.Discharged) - 1
	if e1.Discharged[last][1] != nil {
		t.Errorf("first aggregator, newest bucket = %v, want null: D2 is silent there", *e1.Discharged[last][1])
	}
	if e2.Discharged[last][1] != nil {
		t.Errorf("second aggregator, newest bucket = %v, want null: the total is a gap there", *e2.Discharged[last][1])
	}
	if got := e2.Discharged[0][1]; got == nil || math.Abs(*got-700*5/3.6e6) > 1e-12 {
		t.Errorf("second aggregator, first bucket = %v, want D3 at 700 W for 5 s", got)
	}
	if e1.LatestW != nil || len(e1.LatestMissing) != 1 || e1.LatestMissing[0] != powerD2 {
		t.Errorf("first aggregator latestW = %v missing %v, want null and [D2]", e1.LatestW, e1.LatestMissing)
	}
	if e2.LatestW == nil || *e2.LatestW != 700 {
		t.Errorf("second aggregator latestW = %v, want 700", e2.LatestW)
	}
}

// Record-served buckets of every group equal the live ones, direct included,
// and the Direct group survives a restart that has no readings.
func TestPower_AggregatorEnergyRecordedEqualsLive(t *testing.T) {
	p := newPowerHarness(t)
	twoAggregatorsAndOneDirect(t, p)
	dir := t.TempDir()
	hist := openHistory(t, dir, 48*time.Hour, powerNow)
	if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
		t.Fatal(err)
	}
	if recs := readFileRecords(t, filepath.Join(dir, "power-history-direct.jsonl")); len(recs) == 0 || recs[0].Reporting != 1 {
		t.Fatalf("direct scope records = %+v, want recorded buckets of one device", recs)
	}
	live, _ := aggRoute(t, p.withHistory(nil), "scope=all&window=86400")

	restarted := newPowerHarness(t)
	restarted.manage(powerD1)
	if err := restarted.mgr.Assign(context.Background(), powerAgg2, powerD2); err != nil {
		t.Fatal(err)
	}
	served, _ := aggRoute(t, restarted.withHistory(openHistory(t, dir, 48*time.Hour, powerNow)), "scope=all&window=86400")

	var compared int
	for _, key := range []string{powerAgg, powerAgg2, "direct"} {
		l, s := live.entry(t, key), served.entry(t, key)
		for i := range l.Discharged {
			at := int64(*l.Discharged[i][0])
			if at+120 > historyFinal || l.Discharged[i][1] == nil {
				continue
			}
			compared++
			if !near(l.Discharged[i][1], s.Discharged[i][1]) || !near(l.Charged[i][1], s.Charged[i][1]) {
				t.Errorf("%s at %d: served %v/%v, live %v/%v", key, at, s.Discharged[i][1], s.Charged[i][1], l.Discharged[i][1], l.Charged[i][1])
			}
		}
	}
	if compared < 60 {
		t.Fatalf("compared %d recorded buckets, want at least 60", compared)
	}
}

// An aggregator's own scope has no per-aggregator split, serialized as an
// empty array, and with no direct device and no history the Direct entry is
// absent.
func TestPower_AggregatorEnergyEmptyForFleetScopeAndNoDirect(t *testing.T) {
	p := newPowerHarness(t)
	twoDeviceHour(p)
	_, raw := aggRoute(t, p.withHistory(nil), "aggregatorLFDI="+powerAgg+"&window=900")
	var got struct {
		Entries json.RawMessage `json:"aggregatorEnergy"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Entries) != "[]" {
		t.Errorf("fleet scope aggregatorEnergy = %s, want []", got.Entries)
	}
	a, _ := aggRoute(t, p.withHistory(nil), "scope=all&window=900")
	if len(a.AggregatorEnergy) != 1 || a.AggregatorEnergy[0].Key != powerAgg {
		t.Errorf("entries = %+v, want only the aggregator", a.AggregatorEnergy)
	}
}

// assertStack checks, bucket by bucket, that the groups stack to the energy
// chart: where the total has a point the groups sum to it in both series, and
// where it has none every group is null.
func assertStack(t *testing.T, a aggAnswer) (withPoint int) {
	t.Helper()
	dis, chg := a.energy("Discharged"), a.energy("Charged")
	if dis == nil || chg == nil {
		t.Fatalf("energy series = %+v", a.EnergyChart.Series)
	}
	at := map[int64]int{}
	for i, pt := range dis.Points {
		at[int64(pt[0])/1000] = i
	}
	for _, e := range a.AggregatorEnergy {
		for i := range e.Discharged {
			start := int64(*e.Discharged[i][0])
			if _, ok := at[start]; ok {
				continue
			}
			if e.Discharged[i][1] != nil || e.Charged[i][1] != nil {
				t.Fatalf("%s at %d draws %v/%v where the total has no point", e.Key, start, e.Discharged[i][1], e.Charged[i][1])
			}
		}
	}
	for start, i := range at {
		var sd, sc float64
		for _, e := range a.AggregatorEnergy {
			j := int((start - a.WindowStart) / a.BucketSeconds)
			if int64(*e.Discharged[j][0]) != start {
				t.Fatalf("%s bucket %d starts at %v, want %d", e.Key, j, *e.Discharged[j][0], start)
			}
			if e.Discharged[j][1] != nil {
				sd += *e.Discharged[j][1]
			}
			if e.Charged[j][1] != nil {
				sc += *e.Charged[j][1]
			}
		}
		if math.Abs(sd-dis.Points[i][1]) > 1e-9 || math.Abs(sc-chg.Points[i][1]) > 1e-9 {
			t.Fatalf("bucket %d: groups stack to %v and %v kWh, energyChart has %v and %v", start, sd, sc, dis.Points[i][1], chg.Points[i][1])
		}
		withPoint++
	}
	return withPoint
}

// A device that reports from 100 s ago only: in the 30 s bucket it starts in,
// its group has fewer covered parts than the total, and must still stack to it.
func lateDeviceFleet(t *testing.T, p *powerHarness) {
	t.Helper()
	p.manage(powerD1)
	if err := p.mgr.Assign(context.Background(), powerAgg2, powerD2); err != nil {
		t.Fatal(err)
	}
	const rev = sep2.FlowDirectionReverse
	p.post(powerD1, roleFlagIsDER, 50, rev, readings(powerNow-historyHour, powerNow, constWatts(2000))...)
	p.post(powerD2, roleFlagIsDER, 50, rev, readings(powerNow-100, powerNow, constWatts(1000))...)
}

func TestPower_AggregatorEnergyStacksToTheTotalForALateDevice(t *testing.T) {
	p := newPowerHarness(t)
	lateDeviceFleet(t, p)
	a, _ := aggRoute(t, p.withHistory(nil), "scope=all&window=21600")
	if a.BucketSeconds != 30 {
		t.Fatalf("bucketSeconds = %d, want 30", a.BucketSeconds)
	}
	if n := assertStack(t, a); n < 100 {
		t.Fatalf("only %d buckets had a total point", n)
	}
}

// holeStart is a 120 s bucket boundary before the final edge.
const holeStart = powerNow - 2160

// Two devices whose holes are opposite halves of one 120 s bucket: no 5 s part
// is covered by both, so the total has a gap there and no group draws a bar.
func oppositeHalvesFleet(t *testing.T, p *powerHarness) {
	t.Helper()
	p.manage(powerD1)
	if err := p.mgr.Assign(context.Background(), powerAgg2, powerD2); err != nil {
		t.Fatal(err)
	}
	const rev = sep2.FlowDirectionReverse
	d1 := append(readings(holeStart-1500, holeStart, constWatts(2000)), readings(holeStart+60, powerNow, constWatts(2000))...)
	d2 := append(readings(holeStart-1500, holeStart+1, constWatts(1000)), readings(holeStart+120, powerNow, constWatts(1000))...)
	p.post(powerD1, roleFlagIsDER, 50, rev, d1...)
	p.post(powerD2, roleFlagIsDER, 50, rev, d2...)
}

func TestPower_AggregatorEnergyOppositeHalvesIsAGapInEverySeries(t *testing.T) {
	p := newPowerHarness(t)
	oppositeHalvesFleet(t, p)
	a, _ := aggRoute(t, p.withHistory(nil), "scope=all&window=86400")
	if n := assertStack(t, a); n < 20 {
		t.Fatalf("only %d buckets had a total point", n)
	}
	dis := a.energy("Discharged")
	if _, ok := seriesAt(dis, holeStart); ok {
		t.Fatalf("the total draws a point at %d, want the gap this test builds", holeStart)
	}
	if got := a.entry(t, powerAgg).Discharged[int((holeStart-a.WindowStart)/a.BucketSeconds)][1]; got != nil {
		t.Errorf("group draws %v at the total's gap", *got)
	}
}

// The same stack holds when the buckets are served from the record.
func TestPower_AggregatorEnergyStacksWhenServedFromTheRecord(t *testing.T) {
	for name, build := range map[string]func(*testing.T, *powerHarness){"late": lateDeviceFleet, "opposite": oppositeHalvesFleet} {
		t.Run(name, func(t *testing.T) {
			p := newPowerHarness(t)
			build(t, p)
			dir := t.TempDir()
			hist := openHistory(t, dir, 48*time.Hour, powerNow)
			if err := hist.Record(context.Background(), p.withHistory(hist), powerNow); err != nil {
				t.Fatal(err)
			}
			live, _ := aggRoute(t, p.withHistory(nil), "scope=all&window=86400")
			served, _ := aggRoute(t, p.withHistory(hist), "scope=all&window=86400")
			if n := assertStack(t, served); n < 20 {
				t.Fatalf("only %d buckets had a total point", n)
			}
			for _, e := range live.AggregatorEnergy {
				s := served.entry(t, e.Key)
				for i := range e.Discharged {
					if int64(*e.Discharged[i][0])+120 > historyFinal {
						continue
					}
					if (e.Discharged[i][1] == nil) != (s.Discharged[i][1] == nil) || (e.Discharged[i][1] != nil && (!near(e.Discharged[i][1], s.Discharged[i][1]) || !near(e.Charged[i][1], s.Charged[i][1]))) {
						t.Fatalf("%s bucket %d: served %v/%v, live %v/%v", e.Key, i, s.Discharged[i][1], s.Charged[i][1], e.Discharged[i][1], e.Charged[i][1])
					}
				}
			}
		})
	}
}
