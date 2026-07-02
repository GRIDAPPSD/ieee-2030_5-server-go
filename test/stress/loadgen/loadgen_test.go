package loadgen

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRollingP99_KnownInput exercises the corrected percentile formula with
// a deterministic input and verifies the three design invariants:
//
//  1. Empty sentinel (-1) slots are excluded.
//  2. Genuine 0ns entries are INCLUDED (sub-millisecond loopback is valid).
//  3. Index formula: ceil(n*0.99)-1 (nearest-rank method).
func TestRollingP99_KnownInput(t *testing.T) {
	tests := []struct {
		name    string
		buf     []int64
		wantP99 int64
	}{
		{
			// 100 values 1..100 ns. ceil(100*0.99)-1 = ceil(99)-1 = 98.
			// vals[98] = 99 (0-indexed after sorting).
			name:    "100 sequential values",
			buf:     seq(1, 100),
			wantP99: 99,
		},
		{
			// All empty sentinels: return 0.
			name:    "all empty sentinels",
			buf:     fill(-1, 10),
			wantP99: 0,
		},
		{
			// Mix of empty sentinels and valid zero values.
			// Valid values: [0, 0, 0, 1000000] (4 entries).
			// ceil(4*0.99)-1 = ceil(3.96)-1 = 4-1 = 3 -> vals[3] = 1000000.
			name:    "genuine zero entries included",
			buf:     append(fill(0, 3), append([]int64{-1, -1}, 1000000)...),
			wantP99: 1000000,
		},
		{
			// Single value: p99 of one element is that element.
			name:    "single value",
			buf:     append(fill(-1, 9), 42),
			wantP99: 42,
		},
		{
			// 200 values: 100 sentinels + 100 real values 1..100.
			// n=100, ceil(99)-1=98. vals[98]=99.
			name:    "mixed sentinel and real",
			buf:     append(fill(-1, 100), seq(1, 100)...),
			wantP99: 99,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rollingP99(tc.buf)
			if got != tc.wantP99 {
				t.Errorf("rollingP99(%v...) = %d, want %d", tc.buf[:min(5, len(tc.buf))], got, tc.wantP99)
			}
		})
	}
}

// TestScrapeQueueFull_NonZero verifies that scrapeQueueFull returns a
// non-zero value when the metrics endpoint reports queue_full > 0, and
// returns 0 when the counter is absent.
func TestScrapeQueueFull_NonZero(t *testing.T) {
	const metricsWithQueueFull = `# HELP sep2_subscription_notifications_total Subscription notification delivery outcomes.
# TYPE sep2_subscription_notifications_total counter
sep2_subscription_notifications_total{outcome="success"} 142
sep2_subscription_notifications_total{outcome="client_error"} 3
sep2_subscription_notifications_total{outcome="queue_full"} 7
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(metricsWithQueueFull))
	}))
	defer srv.Close()

	got, err := scrapeQueueFull(srv.URL)
	if err != nil {
		t.Fatalf("scrapeQueueFull returned error: %v", err)
	}
	if got != 7 {
		t.Errorf("scrapeQueueFull = %v, want 7", got)
	}
}

// TestScrapeQueueFull_Zero verifies absence of the metric returns 0.
func TestScrapeQueueFull_Zero(t *testing.T) {
	const metricsWithoutQueueFull = `# HELP sep2_subscription_notifications_total Subscription notification delivery outcomes.
# TYPE sep2_subscription_notifications_total counter
sep2_subscription_notifications_total{outcome="success"} 100
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(metricsWithoutQueueFull))
	}))
	defer srv.Close()

	got, err := scrapeQueueFull(srv.URL)
	if err != nil {
		t.Fatalf("scrapeQueueFull returned error: %v", err)
	}
	if got != 0 {
		t.Errorf("scrapeQueueFull = %v, want 0", got)
	}
}

// --- helpers ---

// seq returns a slice containing [start, start+1, ..., start+count-1].
func seq(start, count int) []int64 {
	out := make([]int64, count)
	for i := range out {
		out[i] = int64(start + i)
	}
	return out
}

// fill returns a slice of n copies of val.
func fill(val int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = val
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
