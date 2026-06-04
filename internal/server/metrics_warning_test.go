package server

import (
	"strings"
	"testing"
)

// HIGH (Leon, PR #264): the metrics listener serves an UNAUTHENTICATED
// /metrics surface. A bare ":9100" pre-fix bound 0.0.0.0/[::] and exposed
// exposition data network-wide. config.ResolveMetricsBind now defaults bare
// ports to loopback (structural fix); metricsExposureWarning is the
// defense-in-depth companion that fires loudly when the operator opts into a
// non-loopback bind (e.g. 0.0.0.0:9100 for a containerized Prometheus
// scraping via host.docker.internal). Pure helper so the test drives it
// directly without capturing log output.
func TestMetricsExposureWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		addr     string
		wantWarn bool
	}{
		// Loopback bind (the IEEE-136 default once ResolveMetricsBind runs):
		// only the host can scrape, so no warning.
		{"loopback 127.0.0.1 → no warn", "127.0.0.1:9100", false},
		{"loopback IPv6 [::1] → no warn", "[::1]:9100", false},

		// Non-loopback bind → fire the warning. These are the explicit
		// operator opt-ins that expose the unauthenticated surface.
		{"public 0.0.0.0 → WARN", "0.0.0.0:9100", true},
		{"public IPv6 [::] → WARN", "[::]:9100", true},
		{"LAN IP 192.168.1.5 → WARN", "192.168.1.5:9100", true},

		// Metrics disabled (empty addr). Caller gates this; assert no warning.
		{"empty addr (metrics off) → no warn", "", false},

		// Unresolved hostname is treated as non-loopback (conservative): a
		// false-positive warning is harmless, a missed warning is not.
		{"hostname unresolved → WARN", "metrics.internal:9100", true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := metricsExposureWarning(tc.addr)
			if tc.wantWarn && got == "" {
				t.Errorf("metricsExposureWarning(%q): expected warning, got empty", tc.addr)
			}
			if !tc.wantWarn && got != "" {
				t.Errorf("metricsExposureWarning(%q): expected empty, got %q", tc.addr, got)
			}
			if tc.wantWarn {
				// Pin load-bearing fragments: the operator MUST see that
				// /metrics is UNAUTHENTICATED, the bind address that tripped
				// it, and the env var to revert to loopback.
				for _, want := range []string{
					"WARNING",
					"UNAUTHENTICATED",
					"SEP2_METRICS_ADDR",
					tc.addr,
				} {
					if !strings.Contains(got, want) {
						t.Errorf("warning missing fragment %q\n---warning---\n%s", want, got)
					}
				}
			}
		})
	}
}
