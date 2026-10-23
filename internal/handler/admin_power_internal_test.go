package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

const (
	powerAgg = "AAAA000000000000000000000000000000000001"
	powerD1  = "BBBB000000000000000000000000000000000001"
	powerD2  = "BBBB000000000000000000000000000000000002"
	// powerNow is a multiple of every bucket size, so the window ends on it.
	powerNow = int64(1_700_000_400)
)

type powerHarness struct {
	t    *testing.T
	mgr  *memory.EndDeviceManagementStore
	mups *memory.Store[sep2.MirrorUsagePoint]
	mmrs *memory.ScopedStore[sep2.MirrorMeterReading]
	n    int
}

func newPowerHarness(t *testing.T) *powerHarness {
	t.Helper()
	old := nowUnix
	nowUnix = func() int64 { return powerNow }
	t.Cleanup(func() { nowUnix = old })
	return &powerHarness{
		t:    t,
		mgr:  memory.NewEndDeviceManagementStore(),
		mups: memory.NewStore[sep2.MirrorUsagePoint](),
		mmrs: memory.NewScopedStore[sep2.MirrorMeterReading](),
	}
}

func (p *powerHarness) manage(managed ...string) {
	p.t.Helper()
	for _, m := range managed {
		if err := p.mgr.Assign(context.Background(), powerAgg, m); err != nil {
			p.t.Fatal(err)
		}
	}
}

// post stores a mirror for deviceLFDI holding one W reading per entry of
// atWatts (receipt time, watts), each export under the 2018 edition.
func (p *powerHarness) post(deviceLFDI string, roles sep2.RoleFlagsValue, postRate uint32, dir uint8, atWatts ...[2]int64) {
	p.t.Helper()
	p.n++
	id := fmt.Sprint(p.n)
	mup := sep2.MirrorUsagePoint{MRID: "MUP" + id, DeviceLFDI: deviceLFDI, PostRate: &postRate, RoleFlags: roles}
	mup.Href = "/mup/" + id
	if err := p.mups.Create(context.Background(), id, mup); err != nil {
		p.t.Fatal(err)
	}
	uom := sep2.UomWatts
	for i, aw := range atWatts {
		v := aw[1]
		r := sep2.MirrorMeterReading{
			MRID:           "W" + id,
			LastUpdateTime: aw[0],
			Reading:        &sep2.Reading{Value: &v},
			ReadingType:    &sep2.ReadingType{Uom: &uom, FlowDirection: &dir},
		}
		if err := p.mmrs.Create(context.Background(), id, fmt.Sprint(i), r); err != nil {
			p.t.Fatal(err)
		}
	}
}

func (p *powerHarness) get(query string) (int, []byte) {
	p.t.Helper()
	h := &AdminFleetHandler{Managers: p.mgr, MirrorUsagePoints: p.mups, MirrorMeterReadings: p.mmrs}
	w := httptest.NewRecorder()
	HandlePower(h, 0)(w, httptest.NewRequest(http.MethodGet, "/api/derms/power?"+query, nil))
	return w.Code, w.Body.Bytes()
}

type powerChartSeries struct {
	Name   string       `json:"name"`
	Points [][2]float64 `json:"points"`
}

type powerAnswer struct {
	Scope           string `json:"scope"`
	AggregatorLFDI  string `json:"aggregatorLFDI"`
	Aggregators     []string
	WindowStart     int64
	WindowEnd       int64
	BucketSeconds   int64
	Devices         []PowerDevice
	NotReporting    []string
	TotalGapBuckets int
	Chart           struct {
		Unit   string
		Series []powerChartSeries
	}
}

func (p *powerHarness) ok(query string) powerAnswer {
	p.t.Helper()
	code, body := p.get(query)
	if code != http.StatusOK {
		p.t.Fatalf("status = %d, body %s", code, body)
	}
	var a powerAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		p.t.Fatal(err)
	}
	return a
}

func (a powerAnswer) series(name string) *powerChartSeries {
	for i := range a.Chart.Series {
		if a.Chart.Series[i].Name == name {
			return &a.Chart.Series[i]
		}
	}
	return nil
}

func (a powerAnswer) device(lfdi string) *PowerDevice {
	for i := range a.Devices {
		if a.Devices[i].LFDI == lfdi {
			return &a.Devices[i]
		}
	}
	return nil
}

// An aggregator posting for two managed devices gets one line per device and
// a Total equal to their sum in the buckets both cover, with no Total point
// where only one is present.
func TestPower_FleetLinePerDeviceAndTotalOnlyWhereBothCover(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	const rev = sep2.FlowDirectionReverse
	// D1 covers [we-100, we-50), D2 covers [we-75, we-25), 50 s holds.
	p.post(powerD1, roleFlagIsDER, 50, rev, [2]int64{powerNow - 100, 1000})
	p.post(powerD2, roleFlagIsDER, 50, rev, [2]int64{powerNow - 75, 2500})
	p.post("EEEE000000000000000000000000000000000001", roleFlagIsDER, 50, rev, [2]int64{powerNow - 100, 7000})

	a := p.ok("aggregatorLFDI=" + powerAgg + "&window=900")
	if a.Scope != "fleet" || a.AggregatorLFDI != powerAgg || a.BucketSeconds != 5 || a.WindowEnd != powerNow || a.WindowStart != powerNow-900 {
		t.Fatalf("header = %+v", a)
	}
	d1, d2, total := a.series(powerD1), a.series(powerD2), a.series("Total")
	if d1 == nil || d2 == nil || total == nil || len(a.Chart.Series) != 3 || a.Chart.Series[0].Name != "Total" || a.Chart.Unit != "W" {
		t.Fatalf("series = %+v", a.Chart.Series)
	}
	if len(d1.Points) != 10 || len(d2.Points) != 10 {
		t.Fatalf("device points = %d and %d, want 10 each", len(d1.Points), len(d2.Points))
	}
	for _, pt := range d1.Points {
		if pt[1] != 1000 {
			t.Fatalf("D1 point %v, want 1000 W", pt)
		}
	}
	if len(total.Points) != 5 {
		t.Fatalf("total points = %v, want the 5 buckets both cover", total.Points)
	}
	for i, pt := range total.Points {
		wantAt := float64((powerNow - 75 + int64(i)*5) * 1000)
		if pt[0] != wantAt || pt[1] != 3500 {
			t.Fatalf("total point %d = %v, want [%v 3500]", i, pt, wantAt)
		}
	}
	if a.TotalGapBuckets != 10 {
		t.Errorf("totalGapBuckets = %d, want 10 (5 covered only by D1, 5 only by D2)", a.TotalGapBuckets)
	}
	for _, lfdi := range []string{powerD1, powerD2} {
		d := a.device(lfdi)
		if d == nil || !d.Drawn || d.CoveredBuckets != 10 {
			t.Errorf("device %s = %+v, want drawn with 10 covered buckets", lfdi, d)
		}
	}
	if len(a.NotReporting) != 1 || a.NotReporting[0] != powerAgg {
		t.Errorf("notReporting = %v, want the aggregator, which posted nothing", a.NotReporting)
	}
}

// Power is export-positive: a Forward (import) reading under the 2018
// edition plots negative.
func TestPower_ImportPlotsNegative(t *testing.T) {
	p := newPowerHarness(t)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionForward, [2]int64{powerNow - 100, 1000})
	a := p.ok("scope=all&window=900")
	if s := a.series(powerD1); s == nil || s.Points[0][1] != -1000 {
		t.Fatalf("series = %+v, want -1000 W", a.Chart.Series)
	}
}

// A device whose only W readings sit on a mirror that is not flagged isDER is
// reported, not drawn, and adds no zero to a Total.
func TestPower_NonDERWattsReportedNotDrawn(t *testing.T) {
	p := newPowerHarness(t)
	p.post(powerD1, 1<<1, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 1000})
	a := p.ok("scope=all&window=900")
	d := a.device(powerD1)
	if d == nil || !d.NonDERWatts || d.Drawn || d.CoveredBuckets != 0 {
		t.Fatalf("device = %+v, want nonDERWatts, not drawn, 0 covered", d)
	}
	if len(a.Chart.Series) != 0 {
		t.Errorf("series = %+v, want none: no reporting device and no zero Total", a.Chart.Series)
	}
	if len(a.NotReporting) != 1 || a.NotReporting[0] != powerD1 {
		t.Errorf("notReporting = %v, want [%s]", a.NotReporting, powerD1)
	}
}

// With 17 reporting devices the 15 largest are drawn beside the Total, which
// still sums all 17; ties break on LFDI.
func TestPower_SeriesCapDrawsLargestAndTotalKeepsAll(t *testing.T) {
	p := newPowerHarness(t)
	want := 0.0
	for i := range 17 {
		w := int64(100 * (i + 1))
		want += float64(w)
		p.post(fmt.Sprintf("CCCC%036d", i), roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, w})
	}
	a := p.ok("scope=all&window=900")
	if len(a.Chart.Series) != 16 {
		t.Fatalf("series = %d, want Total plus 15", len(a.Chart.Series))
	}
	if got := a.series("Total").Points[0][1]; got != want {
		t.Errorf("Total = %v, want %v (all 17 devices)", got, want)
	}
	for i, lfdi := range []string{fmt.Sprintf("CCCC%036d", 0), fmt.Sprintf("CCCC%036d", 1)} {
		if d := a.device(lfdi); d == nil || d.Drawn || d.CoveredBuckets != 10 {
			t.Errorf("small device %d = %+v, want reporting but not drawn", i, d)
		}
		if a.series(lfdi) != nil {
			t.Errorf("small device %d has a series", i)
		}
	}
	if d := a.device(fmt.Sprintf("CCCC%036d", 16)); d == nil || !d.Drawn {
		t.Errorf("largest device = %+v, want drawn", d)
	}
}

// A mirror stored with a lower-case deviceLFDI is the same device as its
// upper-case twin and in a fleet of the upper-case LFDI.
func TestPower_LFDICaseDoesNotSplitDevice(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1)
	p.post(strings.ToLower(powerD1), roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 1000})
	a := p.ok("aggregatorLFDI=" + strings.ToLower(powerAgg) + "&window=900")
	if len(a.Devices) != 2 || a.series(powerD1) == nil || len(a.Chart.Series) != 2 {
		t.Fatalf("devices = %+v series = %+v, want the one canonical device", a.Devices, a.Chart.Series)
	}
}

func TestPower_RefusesBadQueries(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1)
	cases := []struct {
		name, query string
		want        int
	}{
		{"no window", "scope=all", http.StatusBadRequest},
		{"unlisted window", "scope=all&window=1000", http.StatusBadRequest},
		{"non-numeric window", "scope=all&window=hour", http.StatusBadRequest},
		{"neither scope parameter", "window=900", http.StatusBadRequest},
		{"both scope parameters", "scope=all&aggregatorLFDI=" + powerAgg + "&window=900", http.StatusBadRequest},
		{"unknown scope", "scope=fleet&window=900", http.StatusBadRequest},
		{"aggregator not hex", "aggregatorLFDI=nope&window=900", http.StatusBadRequest},
		{"unknown aggregator", "aggregatorLFDI=" + powerD2 + "&window=900", http.StatusNotFound},
		{"a managed device is not an aggregator", "aggregatorLFDI=" + powerD1 + "&window=900", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := p.get(tc.query); code != tc.want {
				t.Fatalf("status = %d, want %d, body %s", code, tc.want, body)
			}
		})
	}
}

// An empty answer carries empty arrays, never null.
func TestPower_EmptyAnswerSerializesEmptyArrays(t *testing.T) {
	p := newPowerHarness(t)
	code, body := p.get("scope=all&window=3600")
	if code != http.StatusOK {
		t.Fatalf("status = %d: %s", code, body)
	}
	for _, want := range []string{`"devices":[]`, `"notReporting":[]`, `"aggregators":[]`, `"series":[]`, `"bucketSeconds":5`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body %s lacks %s", body, want)
		}
	}
}

func TestPowerBucketSeconds(t *testing.T) {
	for window, want := range map[int64]int64{900: 5, 3600: 5, 21600: 30, 86400: 120} {
		if got := powerBucketSeconds(window); got != want {
			t.Errorf("powerBucketSeconds(%d) = %d, want %d", window, got, want)
		}
	}
}

// readingsExpired marks a window reaching past the retention.
func TestPower_ReadingsExpiredWhenWindowOutlastsRetention(t *testing.T) {
	p := newPowerHarness(t)
	h := &AdminFleetHandler{Managers: p.mgr, MirrorUsagePoints: p.mups, MirrorMeterReadings: p.mmrs}
	// nowUnix is pinned, so retention is measured against powerNow.
	for retention, want := range map[time.Duration]bool{0: false, 25 * time.Hour: false, 10 * time.Minute: true} {
		w := httptest.NewRecorder()
		HandlePower(h, retention)(w, httptest.NewRequest(http.MethodGet, "/api/derms/power?scope=all&window=3600", nil))
		var a struct{ ReadingsExpired bool }
		if err := json.Unmarshal(w.Body.Bytes(), &a); err != nil || a.ReadingsExpired != want {
			t.Errorf("retention %v: readingsExpired = %v (%v), want %v", retention, a.ReadingsExpired, err, want)
		}
	}
}

// benchPowerStores seeds the size the design names: 58 devices posting every
// 5 s for 24 h.
func benchPowerStores(b *testing.B) (*memory.Store[sep2.MirrorUsagePoint], *memory.ScopedStore[sep2.MirrorMeterReading]) {
	b.Helper()
	mups := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrs := memory.NewScopedStore[sep2.MirrorMeterReading]()
	ctx := context.Background()
	uom, dir, rate := sep2.UomWatts, sep2.FlowDirectionReverse, uint32(5)
	for d := range 58 {
		id := fmt.Sprint(d)
		mup := sep2.MirrorUsagePoint{MRID: "M" + id, DeviceLFDI: fmt.Sprintf("DDDD%036d", d), PostRate: &rate, RoleFlags: roleFlagIsDER}
		mup.Href = "/mup/" + id
		if err := mups.Create(ctx, id, mup); err != nil {
			b.Fatal(err)
		}
		for i := range 17280 {
			v := int64(1000 + i%50)
			r := sep2.MirrorMeterReading{MRID: "W", LastUpdateTime: powerNow - 86400 + int64(i)*5, Reading: &sep2.Reading{Value: &v}, ReadingType: &sep2.ReadingType{Uom: &uom, FlowDirection: &dir}}
			if err := mmrs.Create(ctx, id, fmt.Sprint(i), r); err != nil {
				b.Fatal(err)
			}
		}
	}
	return mups, mmrs
}

// BenchmarkPower_ListMirrors is a poll that finds no fresh answer: a new
// handler per iteration, so every read lists the store.
func BenchmarkPower_ListMirrors(b *testing.B) {
	old := nowUnix
	nowUnix = func() int64 { return powerNow }
	defer func() { nowUnix = old }()
	mups, mmrs := benchPowerStores(b)
	h := &AdminFleetHandler{Managers: memory.NewEndDeviceManagementStore(), MirrorUsagePoints: mups, MirrorMeterReadings: mmrs}
	b.ResetTimer()
	for range b.N {
		if code, body := serve(HandlePower(h, 0), "scope=all&window=86400"); code != http.StatusOK {
			b.Fatalf("status %d: %s", code, body)
		}
	}
}

// BenchmarkPower_CachedRead is a poll that finds a fresh answer.
func BenchmarkPower_CachedRead(b *testing.B) {
	old := nowUnix
	nowUnix = func() int64 { return powerNow }
	defer func() { nowUnix = old }()
	mups, mmrs := benchPowerStores(b)
	h := HandlePower(&AdminFleetHandler{Managers: memory.NewEndDeviceManagementStore(), MirrorUsagePoints: mups, MirrorMeterReadings: mmrs}, 0)
	if code, body := serve(h, "scope=all&window=86400"); code != http.StatusOK {
		b.Fatalf("status %d: %s", code, body)
	}
	b.ResetTimer()
	for range b.N {
		if code, _ := serve(h, "scope=all&window=86400"); code != http.StatusOK {
			b.Fatal(code)
		}
	}
}
