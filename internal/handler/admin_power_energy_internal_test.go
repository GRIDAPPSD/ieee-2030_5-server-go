package handler

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// A device that discharges 5 kW for 2 s and charges 5 kW for 3 s inside one
// 5 s bucket reports both parts, not their net.
func TestIntegrateSigned_SplitsSignInsideOneBucket(t *testing.T) {
	spans := map[leg][]powerSpan{{}: {
		{start: 0, end: 2, watts: 5000, order: 0},
		{start: 2, end: 5, watts: -5000, order: 1},
	}}
	seen := map[leg]bool{{}: true}
	sums, signed, _ := integrateSigned(spans, seen, []int64{0, 5})
	if got := signed[0]; got.exportWs != 10000 || got.importWs != -15000 {
		t.Fatalf("signed = %+v, want export 10000 Ws and import -15000 Ws", got)
	}
	if sums[0].wattSeconds != -5000 || sums[0].covered != 5 {
		t.Fatalf("sums = %+v, want the net -5000 Ws over 5 s", sums[0])
	}
	if got := signed[0].exportWs + signed[0].importWs; got != sums[0].wattSeconds {
		t.Fatalf("export+import = %v, want wattSeconds %v", got, sums[0].wattSeconds)
	}
}

// The sign is taken on one device's path net, so a total path of two legs
// that cancel reports neither part.
func TestIntegrateSigned_PathNetCancelsBeforeSplit(t *testing.T) {
	fwd, rev := leg{average: true, dir: dirForward}, leg{average: true, dir: dirReverse}
	spans := map[leg][]powerSpan{
		fwd: {{start: 0, end: 5, watts: -300, order: 0}},
		rev: {{start: 0, end: 5, watts: 300, order: 1}},
	}
	seen := map[leg]bool{fwd: true, rev: true}
	_, signed, _ := integrateSigned(spans, seen, []int64{0, 5})
	if signed[0].exportWs != 0 || signed[0].importWs != 0 {
		t.Fatalf("signed = %+v, want no export and no import for a zero net", signed[0])
	}
}

type energyAnswer struct {
	powerAnswer
	EnergyChart struct {
		Unit   string
		Series []powerChartSeries
	}
}

func (p *powerHarness) energy(query string) energyAnswer {
	p.t.Helper()
	code, body := p.get(query)
	if code != 200 {
		p.t.Fatalf("status = %d, body %s", code, body)
	}
	var a energyAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		p.t.Fatal(err)
	}
	return a
}

func (a energyAnswer) energySeries(name string) *powerChartSeries {
	for i := range a.EnergyChart.Series {
		if a.EnergyChart.Series[i].Name == name {
			return &a.EnergyChart.Series[i]
		}
	}
	return nil
}

// One device discharges while the other charges in the same buckets: the
// energy chart keeps both, and in every bucket Discharged plus Charged equals
// Total W times the bucket length.
func TestPower_EnergyKeepsChargeAndDischargeAndMatchesTotal(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	p.post(powerD1, roleFlagIsDER, 50, sep2.FlowDirectionReverse, [2]int64{powerNow - 100, 2000})
	p.post(powerD2, roleFlagIsDER, 50, sep2.FlowDirectionForward, [2]int64{powerNow - 100, 1000})

	a := p.energy("aggregatorLFDI=" + powerAgg + "&window=900")
	if a.EnergyChart.Unit != "kWh" || len(a.EnergyChart.Series) != 2 {
		t.Fatalf("energy chart = %+v, want kWh with 2 series", a.EnergyChart)
	}
	dis, chg, total := a.energySeries("Discharged"), a.energySeries("Charged"), a.series("Total")
	if dis == nil || chg == nil || total == nil {
		t.Fatalf("series missing: %+v", a.EnergyChart.Series)
	}
	if len(dis.Points) != 10 || len(chg.Points) != 10 || len(total.Points) != 10 {
		t.Fatalf("points = %d, %d and %d, want 10 each", len(dis.Points), len(chg.Points), len(total.Points))
	}
	const perBucket = 5.0 / 3.6e6
	for i := range dis.Points {
		if dis.Points[i][0] != total.Points[i][0] || chg.Points[i][0] != total.Points[i][0] {
			t.Fatalf("point %d times differ: %v %v %v", i, dis.Points[i], chg.Points[i], total.Points[i])
		}
		if math.Abs(dis.Points[i][1]-2000*perBucket) > 1e-12 || math.Abs(chg.Points[i][1]+1000*perBucket) > 1e-12 {
			t.Fatalf("bucket %d = discharged %v charged %v kWh, want %v and %v", i, dis.Points[i][1], chg.Points[i][1], 2000*perBucket, -1000*perBucket)
		}
		if got, want := dis.Points[i][1]+chg.Points[i][1], total.Points[i][1]*perBucket; math.Abs(got-want) > 1e-12 {
			t.Fatalf("bucket %d: discharged+charged = %v kWh, total W * bucket = %v kWh", i, got, want)
		}
	}
}

// A bucket covered by only one of two reporting devices has no energy point,
// the same buckets the Total skips, and the gap counter does not change.
func TestPower_EnergyHasNoPointWhereAReportingDeviceIsMissing(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1, powerD2)
	const rev = sep2.FlowDirectionReverse
	p.post(powerD1, roleFlagIsDER, 50, rev, [2]int64{powerNow - 100, 1000})
	p.post(powerD2, roleFlagIsDER, 50, rev, [2]int64{powerNow - 75, 2500})

	a := p.energy("aggregatorLFDI=" + powerAgg + "&window=900")
	dis, chg, total := a.energySeries("Discharged"), a.energySeries("Charged"), a.series("Total")
	if dis == nil || chg == nil || total == nil || len(dis.Points) != 5 || len(chg.Points) != 5 || len(total.Points) != 5 {
		t.Fatalf("energy series = %+v, want 5 points each, as the Total", a.EnergyChart.Series)
	}
	for i, pt := range dis.Points {
		if pt[0] != total.Points[i][0] || math.Abs(pt[1]-3500*5/3.6e6) > 1e-12 || chg.Points[i][1] != 0 {
			t.Fatalf("point %d = discharged %v charged %v, want [%v 3500 W for 5 s] and 0", i, pt, chg.Points[i], total.Points[i][0])
		}
	}
	if a.TotalGapBuckets != 10 {
		t.Errorf("totalGapBuckets = %d, want 10 as before", a.TotalGapBuckets)
	}
}

// With no reporting device the energy chart is present and empty, like the
// W chart, never a zero point.
func TestPower_EnergyChartEmptyWithoutReportingDevices(t *testing.T) {
	p := newPowerHarness(t)
	p.manage(powerD1)
	a := p.energy("aggregatorLFDI=" + powerAgg + "&window=900")
	if a.EnergyChart.Unit != "kWh" || len(a.EnergyChart.Series) != 0 {
		t.Fatalf("energy chart = %+v, want kWh and no series", a.EnergyChart)
	}
}
