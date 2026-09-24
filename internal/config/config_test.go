package config

import (
	"path/filepath"
	"testing"
)

// #622: EffectiveServingCA/EffectiveDeviceCA are the shared-default seam a
// deployment that never sets the new settings depends on: both must resolve
// to the pre-split CAFile so item 1's "byte for byte" claim is a fact about
// this function, not just about main.go's wiring of it.
func TestEffectiveServingAndDeviceCA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		cfg         Config
		wantServing string
		wantDevice  string
	}{
		{
			name:        "neither set falls back to CAFile for both",
			cfg:         Config{CAFile: "/etc/tls/ca.crt"},
			wantServing: "/etc/tls/ca.crt",
			wantDevice:  "/etc/tls/ca.crt",
		},
		{
			name:        "only ServingCAFile set: device still falls back to CAFile",
			cfg:         Config{CAFile: "/etc/tls/ca.crt", ServingCAFile: "/etc/tls/serving-ca.crt"},
			wantServing: "/etc/tls/serving-ca.crt",
			wantDevice:  "/etc/tls/ca.crt",
		},
		{
			name:        "only DeviceCAFile set: serving still falls back to CAFile",
			cfg:         Config{CAFile: "/etc/tls/ca.crt", DeviceCAFile: "/etc/tls/device-ca.crt"},
			wantServing: "/etc/tls/ca.crt",
			wantDevice:  "/etc/tls/device-ca.crt",
		},
		{
			name:        "both set: each wins over CAFile independently",
			cfg:         Config{CAFile: "/etc/tls/ca.crt", ServingCAFile: "/etc/tls/serving-ca.crt", DeviceCAFile: "/etc/tls/device-ca.crt"},
			wantServing: "/etc/tls/serving-ca.crt",
			wantDevice:  "/etc/tls/device-ca.crt",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.EffectiveServingCA(); got != tc.wantServing {
				t.Errorf("EffectiveServingCA() = %q, want %q", got, tc.wantServing)
			}
			if got := tc.cfg.EffectiveDeviceCA(); got != tc.wantDevice {
				t.Errorf("EffectiveDeviceCA() = %q, want %q", got, tc.wantDevice)
			}
		})
	}
}

// #624: EffectiveAdminClientCA is the trust-anchor seam buildAdminTLSConfig
// reads for the admin listener's ClientCAs pool. Unset falls back to the
// serving CA (the CA that signs the operator cert, per #622's role split);
// the AdminClientCASystemRoots sentinel is returned unresolved so the
// caller building the *tls.Config leaves ClientCAs nil rather than treating
// "system" as a file path.
func TestEffectiveAdminClientCA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "unset falls back to serving CA",
			cfg:  Config{CAFile: "/etc/tls/ca.crt", ServingCAFile: "/etc/tls/serving-ca.crt"},
			want: "/etc/tls/serving-ca.crt",
		},
		{
			name: "unset with no serving CA falls back to CAFile",
			cfg:  Config{CAFile: "/etc/tls/ca.crt"},
			want: "/etc/tls/ca.crt",
		},
		{
			name: "explicit path wins over the serving CA default",
			cfg:  Config{CAFile: "/etc/tls/ca.crt", ServingCAFile: "/etc/tls/serving-ca.crt", AdminClientCA: "/etc/tls/public-ca.crt"},
			want: "/etc/tls/public-ca.crt",
		},
		{
			name: "the system-roots sentinel passes through unresolved",
			cfg:  Config{CAFile: "/etc/tls/ca.crt", ServingCAFile: "/etc/tls/serving-ca.crt", AdminClientCA: AdminClientCASystemRoots},
			want: "system",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.cfg.EffectiveAdminClientCA(); got != tc.want {
				t.Errorf("EffectiveAdminClientCA() = %q, want %q", got, tc.want)
			}
		})
	}
}

// #268: ResolveAdminBind applies a loopback default to a bare-port
// admin listen string. Pre-#268, ":8444" handed to net.Listen bound
// 0.0.0.0 - any network neighbor (or co-resident process on a multi-
// tenant host) could reach the admin surface, and the AdminAuthMiddleware
// Path 0 loopback bypass would admit them. The fix: bare-port -> loopback
// by default; any explicit host (0.0.0.0, an LAN IP, IPv6 wildcard, a
// hostname) passes through unchanged. Operators who want network
// exposure must say so explicitly.
func TestResolveAdminBind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		// Disabled stays disabled. The caller gates this case before
		// reaching net.Listen; ResolveAdminBind does not invent a bind.
		{"empty (admin disabled)", "", ""},

		// #268 core: bare port gets the loopback default. This is
		// the behavior change the card is for.
		{"bare port :8444 -> loopback", ":8444", "127.0.0.1:8444"},
		{"bare port :9443 -> loopback", ":9443", "127.0.0.1:9443"},
		{"bare port :0 (ephemeral) -> loopback", ":0", "127.0.0.1:0"},

		// Explicit 0.0.0.0 is the operator saying "I want public
		// network exposure" - pass through. No silent rewriting.
		{"explicit 0.0.0.0:port unchanged", "0.0.0.0:8444", "0.0.0.0:8444"},

		// Explicit IPv6 wildcard same story as 0.0.0.0.
		{"explicit [::]:port unchanged", "[::]:8444", "[::]:8444"},

		// Explicit loopback host stays explicit. No-op.
		{"explicit 127.0.0.1:port unchanged", "127.0.0.1:8444", "127.0.0.1:8444"},
		{"explicit [::1]:port unchanged", "[::1]:8444", "[::1]:8444"},

		// LAN IP / management network - the operator explicitly said
		// "this interface". Honor it.
		{"explicit LAN IP unchanged", "192.168.1.5:8444", "192.168.1.5:8444"},
		{"explicit hostname unchanged", "admin.internal:8444", "admin.internal:8444"},

		// Malformed input passes through; net.Listen will reject and
		// the caller surfaces a clear error. We don't try to repair.
		{"malformed (no port) unchanged", "8444", "8444"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveAdminBind(tc.in); got != tc.want {
				t.Errorf("ResolveAdminBind(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ResolveMetricsBind mirrors ResolveAdminBind's loopback-default contract for
// the UNAUTHENTICATED /metrics listener (no client cert, no Bearer). A bare
// ":9100" pre-fix bound 0.0.0.0/[::] and exposed exposition data network-wide
// (Leon HIGH); the fix defaults bare ports to loopback so wide-open bind is
// explicit opt-in. The two resolvers share an implementation, so this test
// pins the metrics-facing contract independently and guards against drift.
func TestResolveMetricsBind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty (metrics disabled)", "", ""},

		// HIGH fix core: bare port -> loopback default.
		{"bare port :9100 -> loopback", ":9100", "127.0.0.1:9100"},
		{"bare port :0 (ephemeral) -> loopback", ":0", "127.0.0.1:0"},

		// Explicit routable forms pass through (the documented
		// host.docker.internal scrape path). No silent rewrite.
		{"explicit 0.0.0.0:port unchanged", "0.0.0.0:9100", "0.0.0.0:9100"},
		{"explicit [::]:port unchanged", "[::]:9100", "[::]:9100"},
		{"explicit LAN IP unchanged", "192.168.1.5:9100", "192.168.1.5:9100"},

		// Explicit loopback stays explicit.
		{"explicit 127.0.0.1:port unchanged", "127.0.0.1:9100", "127.0.0.1:9100"},

		// Malformed passes through; net.Listen rejects.
		{"malformed (no port) unchanged", "9100", "9100"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolveMetricsBind(tc.in); got != tc.want {
				t.Errorf("ResolveMetricsBind(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// #165: EffectiveStorePath resolves a per-store on-disk path with the
// precedence rule:
//
//  1. If dedicatedPath is non-empty, return it verbatim (back-compat for
//     SEP2_SUBSCRIPTION_STORE_PATH etc.).
//  2. Else if DataDir is non-empty, return <DataDir>/<name>.json.
//  3. Else return "" - in-memory mode.
//
// Empty Config (no DataDir, no dedicated path) keeps the historical pre-
// #165 behavior so every existing test path stays unchanged.
func TestEffectiveStorePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		dataDir       string
		dedicatedPath string
		storeName     string
		want          string
	}{
		{
			name:          "neither set returns empty (in-memory)",
			dataDir:       "",
			dedicatedPath: "",
			storeName:     "subscriptions",
			want:          "",
		},
		{
			name:          "DataDir only derives storeName.json",
			dataDir:       "/var/lib/sep2",
			dedicatedPath: "",
			storeName:     "subscriptions",
			want:          filepath.Join("/var/lib/sep2", "subscriptions.json"),
		},
		{
			name:          "dedicated path only returns verbatim",
			dataDir:       "",
			dedicatedPath: "/custom/subs.json",
			storeName:     "subscriptions",
			want:          "/custom/subs.json",
		},
		{
			name:          "dedicated path wins over DataDir",
			dataDir:       "/var/lib/sep2",
			dedicatedPath: "/custom/subs.json",
			storeName:     "subscriptions",
			want:          "/custom/subs.json",
		},
		{
			name:          "DataDir works for enddevices",
			dataDir:       "/data",
			dedicatedPath: "",
			storeName:     "enddevices",
			want:          filepath.Join("/data", "enddevices.json"),
		},
		{
			// #165 precedence regression: SEP2_SUBSCRIPTION_STORE_PATH
			// (the #224-era dedicated knob) must keep working even
			// when SEP2_DATA_DIR is also set. Dedicated path wins.
			name:          "SubscriptionStorePath wins over DataDir-derived path (back-compat)",
			dataDir:       "/data/sep2",
			dedicatedPath: "/legacy/subs.json",
			storeName:     "subscriptions",
			want:          "/legacy/subs.json",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{DataDir: tc.dataDir}
			got := cfg.EffectiveStorePath(tc.storeName, tc.dedicatedPath)
			if got != tc.want {
				t.Errorf("EffectiveStorePath(%q, %q) with DataDir=%q = %q, want %q",
					tc.storeName, tc.dedicatedPath, tc.dataDir, got, tc.want)
			}
		})
	}
}

// TestEffectiveTrafficDir pins #611's directory precedence: TrafficDir wins,
// else <DataDir>/traffic, else "" (capture off). Mutant: swap the two
// EffectiveTrafficDir branches (return the DataDir join first) and this
// test's "TrafficDir wins over DataDir" case goes red.
func TestEffectiveTrafficDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		trafficDir string
		dataDir    string
		want       string
	}{
		{
			name: "neither set returns empty (capture off)",
			want: "",
		},
		{
			name:    "DataDir only derives <DataDir>/traffic",
			dataDir: "/var/lib/sep2",
			want:    filepath.Join("/var/lib/sep2", "traffic"),
		},
		{
			name:       "TrafficDir only returns verbatim",
			trafficDir: "/custom/traffic",
			want:       "/custom/traffic",
		},
		{
			name:       "TrafficDir wins over DataDir",
			trafficDir: "/custom/traffic",
			dataDir:    "/var/lib/sep2",
			want:       "/custom/traffic",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{TrafficDir: tc.trafficDir, DataDir: tc.dataDir}
			if got := cfg.EffectiveTrafficDir(); got != tc.want {
				t.Errorf("EffectiveTrafficDir() with TrafficDir=%q DataDir=%q = %q, want %q",
					tc.trafficDir, tc.dataDir, got, tc.want)
			}
		})
	}
}
