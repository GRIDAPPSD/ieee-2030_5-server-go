package sep2admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// chartRefusals maps the refusal codes in testdata/chart_cases.json, which
// the admin UI reads too, to this package's sentinels.
var chartRefusals = map[string]error{
	"too-many-series":     ErrChartTooManySeries,
	"series-too-long":     ErrChartSeriesTooLong,
	"too-many-points":     ErrChartTooManyPoints,
	"too-many-sections":   ErrChartTooManySections,
	"value-not-finite":    ErrChartValueNotFinite,
	"points-out-of-order": ErrChartPointsOutOfOrder,
	"series-without-name": ErrChartSeriesNoName,
	"time-out-of-range":   ErrChartTimeOutOfRange,
}

type chartCase struct {
	Name    string `json:"name"`
	Refusal string `json:"refusal"`
	// RendererOnly marks a wire value the encoder cannot produce, such as
	// a fractional millisecond time; only the admin UI runs it.
	RendererOnly bool              `json:"rendererOnly"`
	Generate     [][]int           `json:"generate"`
	Charts       []json.RawMessage `json:"charts"`
}

// chartCaseDescriptor builds the Descriptor a shared case describes.
func chartCaseDescriptor(t *testing.T, c chartCase) Descriptor {
	t.Helper()
	d := Descriptor{Version: CurrentDescriptorVersion}
	for _, lengths := range c.Generate {
		body := ChartBody{Unit: "u"}
		for k, n := range lengths {
			s := ChartSeries{Name: fmt.Sprintf("s%d", k)}
			for j := range n {
				s.Points = append(s.Points, ChartPoint{At: time.UnixMilli(1759343400000 + 60000*int64(j)), Value: float64(j)})
			}
			body.Series = append(body.Series, s)
		}
		d.Sections = append(d.Sections, Section{Body: NewChartBody(body)})
	}
	for _, raw := range c.Charts {
		d.Sections = append(d.Sections, Section{Body: NewChartBody(decodeWireChart(t, raw))})
	}
	return d
}

func decodeWireChart(t *testing.T, raw json.RawMessage) ChartBody {
	t.Helper()
	var w struct {
		Unit   string `json:"unit"`
		Series []struct {
			Name   string               `json:"name"`
			Points [][2]json.RawMessage `json:"points"`
		} `json:"series"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		t.Fatalf("decode chart %s: %v", raw, err)
	}
	body := ChartBody{Unit: w.Unit}
	for _, ws := range w.Series {
		s := ChartSeries{Name: ws.Name}
		for _, p := range ws.Points {
			var ms json.Number
			if err := json.Unmarshal(p[0], &ms); err != nil {
				t.Fatalf("point time %s: %v", p[0], err)
			}
			at, err := ms.Int64()
			if err != nil {
				t.Fatalf("point time %s: %v", p[0], err)
			}
			s.Points = append(s.Points, ChartPoint{At: time.UnixMilli(at), Value: fixtureValue(t, p[1])})
		}
		body.Series = append(body.Series, s)
	}
	return body
}

// fixtureValue reads a point value: a JSON number, or one of the three
// strings standing for the values JSON cannot carry.
func fixtureValue(t *testing.T, raw json.RawMessage) float64 {
	t.Helper()
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case "NaN":
			return math.NaN()
		case "Infinity":
			return math.Inf(1)
		case "-Infinity":
			return math.Inf(-1)
		}
		t.Fatalf("point value %s is not a number or a named non-finite value", raw)
	}
	var v float64
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("point value %s: %v", raw, err)
	}
	return v
}

func TestChartSharedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/chart_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Bounds struct {
			SeriesPerSection           int `json:"seriesPerSection"`
			PointsPerSeries            int `json:"pointsPerSeries"`
			PointsPerDescriptor        int `json:"pointsPerDescriptor"`
			ChartSectionsPerDescriptor int `json:"chartSectionsPerDescriptor"`
		} `json:"bounds"`
		Cases []chartCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if b := doc.Bounds; b.SeriesPerSection != MaxChartSeries || b.PointsPerSeries != MaxChartSeriesPoints || b.PointsPerDescriptor != MaxDescriptorChartPoints || b.ChartSectionsPerDescriptor != MaxDescriptorChartSections {
		t.Fatalf("fixture bounds %+v differ from the encoder's %d, %d, %d, %d", b, MaxChartSeries, MaxChartSeriesPoints, MaxDescriptorChartPoints, MaxDescriptorChartSections)
	}
	seen := map[string]bool{}
	for _, c := range doc.Cases {
		if c.RendererOnly {
			if _, ok := chartRefusals[c.Refusal]; ok || c.Refusal == "" {
				t.Errorf("renderer-only case %q has refusal %q; want a code the encoder never returns", c.Name, c.Refusal)
			}
			continue
		}
		t.Run(c.Name, func(t *testing.T) {
			d := chartCaseDescriptor(t, c)
			b, err := json.Marshal(d)
			if c.Refusal == "" {
				if err != nil {
					t.Fatalf("Marshal: %v, want accepted", err)
				}
				assertChartPointsCarried(t, d, b)
				return
			}
			seen[c.Refusal] = true
			want, ok := chartRefusals[c.Refusal]
			if !ok {
				t.Fatalf("unknown refusal code %q", c.Refusal)
			}
			if !errors.Is(err, want) || b != nil {
				t.Fatalf("Marshal = %d bytes, %v; want nil bytes and %v", len(b), err, want)
			}
		})
	}
	for code := range chartRefusals {
		if !seen[code] {
			t.Errorf("no shared case exercises refusal %q", code)
		}
	}
}

// assertChartPointsCarried reads the encoded chart sections back and
// requires every series name and point to match what was encoded.
func assertChartPointsCarried(t *testing.T, d Descriptor, b []byte) {
	t.Helper()
	var got struct {
		Sections []struct {
			Kind string `json:"kind"`
			Body struct {
				Unit   string `json:"unit"`
				Series []struct {
					Name   string       `json:"name"`
					Points [][2]float64 `json:"points"`
				} `json:"series"`
			} `json:"body"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Sections) != len(d.Sections) {
		t.Fatalf("%d sections encoded, want %d", len(got.Sections), len(d.Sections))
	}
	for i, s := range d.Sections {
		gs := got.Sections[i]
		if gs.Kind != "chart" || gs.Body.Unit != s.Body.chart.Unit || len(gs.Body.Series) != len(s.Body.chart.Series) {
			t.Fatalf("section %d = kind %q unit %q, %d series; want chart %q, %d series", i, gs.Kind, gs.Body.Unit, len(gs.Body.Series), s.Body.chart.Unit, len(s.Body.chart.Series))
		}
		for k, series := range s.Body.chart.Series {
			gotSeries := gs.Body.Series[k]
			if gotSeries.Name != series.Name || len(gotSeries.Points) != len(series.Points) {
				t.Fatalf("section %d series %d = %q with %d points, want %q with %d", i, k, gotSeries.Name, len(gotSeries.Points), series.Name, len(series.Points))
			}
			for j, p := range series.Points {
				if gp := gotSeries.Points[j]; gp[0] != float64(p.At.UnixMilli()) || gp[1] != p.Value {
					t.Fatalf("section %d series %d point %d = %v, want [%d %v]", i, k, j, gp, p.At.UnixMilli(), p.Value)
				}
			}
		}
	}
}

func oneChart(points ...ChartPoint) Descriptor {
	return Descriptor{Version: CurrentDescriptorVersion, Sections: []Section{{
		Body: NewChartBody(ChartBody{Series: []ChartSeries{{Name: "a", Points: points}}}),
	}}}
}

// TestChartRefusalsTheWireCannotCarry covers inputs that exist only as Go
// values, so the shared fixture cannot hold them.
func TestChartRefusalsTheWireCannotCarry(t *testing.T) {
	cases := []struct {
		name string
		d    Descriptor
		want error
	}{
		{"zero time", oneChart(ChartPoint{Value: 1}), ErrChartZeroTime},
		// 18446744073709552 s is 2^64 + 384 ms, which UnixMilli wraps to
		// 384: in range unless seconds are checked first.
		{"time past int64 milliseconds", oneChart(ChartPoint{At: time.Unix(18446744073709552, 0), Value: 1}), ErrChartTimeOutOfRange},
		{"duplicate millisecond with distinct nanoseconds", oneChart(
			ChartPoint{At: chartAt, Value: 1},
			ChartPoint{At: chartAt.Add(999 * time.Microsecond), Value: 2},
		), ErrChartPointsOutOfOrder},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.d)
			if !errors.Is(err, tc.want) || b != nil {
				t.Fatalf("Marshal = %s, %v; want nil bytes and %v", b, err, tc.want)
			}
		})
	}
}

// TestChartRefusalFailsTheWholeDescriptor: a renderer never receives a
// payload with the bad chart dropped.
func TestChartRefusalFailsTheWholeDescriptor(t *testing.T) {
	d := fixtureDescriptor()
	d.Sections[2].Body.chart.Series[0].Points[1].Value = math.NaN()
	b, err := json.Marshal(d)
	if !errors.Is(err, ErrChartValueNotFinite) || b != nil {
		t.Fatalf("Marshal = %s, %v; want nil bytes and ErrChartValueNotFinite", b, err)
	}
	if !strings.Contains(err.Error(), "sections[2]: series[0].points[1]") {
		t.Errorf("error %q does not locate the point", err)
	}
}

// TestChartSeriesNameIsCarriedUnchanged: the encoder does not escape a
// series name into markup or strip it; the renderer escapes at render.
func TestChartSeriesNameIsCarriedUnchanged(t *testing.T) {
	names := []string{hostileSeriesName, `a & b "c" 'd'`, "&lt;img&gt;", " padded "}
	for _, name := range names {
		d := oneChart(ChartPoint{At: chartAt, Value: 1})
		d.Sections[0].Body.chart.Series[0].Name = name
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("name %q: %v", name, err)
		}
		var got struct {
			Sections []struct {
				Body struct {
					Series []struct {
						Name string `json:"name"`
					} `json:"series"`
				} `json:"body"`
			} `json:"sections"`
		}
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got.Sections[0].Body.Series[0].Name != name {
			t.Errorf("name round-tripped as %q, want %q", got.Sections[0].Body.Series[0].Name, name)
		}
	}
}

// TestChartPointsAreNotRows: chart points have their own bound and do not
// count against the admin plane's row cap.
func TestChartPointsAreNotRows(t *testing.T) {
	d := fixtureDescriptor()
	if got := d.RowCount(); got != 6 {
		t.Fatalf("RowCount = %d, want 6: 2 table rows and 4 definition entries, no chart points", got)
	}
}
