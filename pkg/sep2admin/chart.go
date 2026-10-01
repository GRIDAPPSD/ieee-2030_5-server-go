package sep2admin

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Chart bounds, refused at encode. 720 points is a day at two-minute
// samples. 12000 points across a Descriptor encode to about 0.5 MB at
// worst, under the admin plane's 1 MiB response cap.
const (
	MaxChartSeries           = 16
	MaxChartSeriesPoints     = 720
	MaxDescriptorChartPoints = 12000
)

// maxChartMillis is the largest instant, in Unix milliseconds either side
// of the epoch, a JavaScript Date can hold. Every such value is also an
// exact JSON number for a JavaScript reader.
const maxChartMillis = 8_640_000_000_000_000

// Refusals for a ChartBody, each checked with errors.Is.
var (
	ErrChartTooManySeries    = errors.New("sep2admin: chart has more series than MaxChartSeries")
	ErrChartSeriesTooLong    = errors.New("sep2admin: chart series has more points than MaxChartSeriesPoints")
	ErrChartTooManyPoints    = errors.New("sep2admin: Descriptor has more chart points than MaxDescriptorChartPoints")
	ErrChartSeriesNoName     = errors.New("sep2admin: chart series has an empty name")
	ErrChartValueNotFinite   = errors.New("sep2admin: chart point value is NaN or infinite")
	ErrChartZeroTime         = errors.New("sep2admin: chart point has the zero time")
	ErrChartTimeOutOfRange   = errors.New("sep2admin: chart point time is outside the range a JavaScript Date holds")
	ErrChartPointsOutOfOrder = errors.New("sep2admin: chart points are not in strictly ascending time order")
)

// ChartBody is the chart shape: series sharing one Unit. Mixed units are
// separate sections.
type ChartBody struct {
	Unit   string
	Series []ChartSeries
}

// ChartSeries is one named line. Name is literal text, escaped only by the
// renderer. Points are in strictly ascending time order, compared at
// millisecond precision, so two points in the same millisecond are
// refused: a renderer would draw them as one instant with two values.
type ChartSeries struct {
	Name   string
	Points []ChartPoint
}

// ChartPoint is one sample. At goes on the wire as Unix milliseconds.
type ChartPoint struct {
	At    time.Time
	Value float64
}

type wireChart struct {
	Unit   string            `json:"unit"`
	Series []wireChartSeries `json:"series"`
}

type wireChartSeries struct {
	Name   string   `json:"name"`
	Points [][2]any `json:"points"`
}

// wireChartOf checks c and adds its points to *total, the running count
// across the Descriptor. The per-series length is checked before the
// series is walked, so an oversized View is refused without reading it.
func wireChartOf(c ChartBody, total *int) (wireChart, error) {
	if len(c.Series) > MaxChartSeries {
		return wireChart{}, fmt.Errorf("%w: %d", ErrChartTooManySeries, len(c.Series))
	}
	series := make([]wireChartSeries, 0, len(c.Series))
	for i, s := range c.Series {
		if s.Name == "" {
			return wireChart{}, fmt.Errorf("series[%d]: %w", i, ErrChartSeriesNoName)
		}
		if len(s.Points) > MaxChartSeriesPoints {
			return wireChart{}, fmt.Errorf("series[%d]: %w: %d", i, ErrChartSeriesTooLong, len(s.Points))
		}
		if *total += len(s.Points); *total > MaxDescriptorChartPoints {
			return wireChart{}, fmt.Errorf("series[%d]: %w: %d", i, ErrChartTooManyPoints, *total)
		}
		points := make([][2]any, 0, len(s.Points))
		var last int64
		for j, p := range s.Points {
			ms, err := chartMillis(p)
			if err != nil {
				return wireChart{}, fmt.Errorf("series[%d].points[%d]: %w", i, j, err)
			}
			if j > 0 && ms <= last {
				return wireChart{}, fmt.Errorf("series[%d].points[%d]: %w: %d after %d", i, j, ErrChartPointsOutOfOrder, ms, last)
			}
			last = ms
			points = append(points, [2]any{ms, p.Value})
		}
		series = append(series, wireChartSeries{Name: s.Name, Points: points})
	}
	return wireChart{Unit: c.Unit, Series: series}, nil
}

func chartMillis(p ChartPoint) (int64, error) {
	if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
		return 0, fmt.Errorf("%w: %v", ErrChartValueNotFinite, p.Value)
	}
	if p.At.IsZero() {
		return 0, ErrChartZeroTime
	}
	// Seconds are compared first: UnixMilli wraps for an instant past
	// int64 milliseconds, and a wrapped value could land back in range.
	if sec := p.At.Unix(); sec > maxChartMillis/1000 || sec < -maxChartMillis/1000 {
		return 0, fmt.Errorf("%w: %s", ErrChartTimeOutOfRange, p.At.UTC().Format(time.RFC3339))
	}
	ms := p.At.UnixMilli()
	if ms > maxChartMillis || ms < -maxChartMillis {
		return 0, fmt.Errorf("%w: %d", ErrChartTimeOutOfRange, ms)
	}
	return ms, nil
}
