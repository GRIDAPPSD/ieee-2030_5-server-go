package config

import (
	"path/filepath"
	"testing"
)

// IEEE-136: ResolveAdminBind applies a loopback default to a bare-port
// admin listen string. Pre-IEEE-136, ":8444" handed to net.Listen bound
// 0.0.0.0 — any network neighbor (or co-resident process on a multi-
// tenant host) could reach the admin surface, and the AdminAuthMiddleware
// Path 0 loopback bypass would admit them. The fix: bare-port → loopback
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

		// IEEE-136 core: bare port gets the loopback default. This is
		// the behavior change the card is for.
		{"bare port :8444 → loopback", ":8444", "127.0.0.1:8444"},
		{"bare port :9443 → loopback", ":9443", "127.0.0.1:9443"},
		{"bare port :0 (ephemeral) → loopback", ":0", "127.0.0.1:0"},

		// Explicit 0.0.0.0 is the operator saying "I want public
		// network exposure" — pass through. No silent rewriting.
		{"explicit 0.0.0.0:port unchanged", "0.0.0.0:8444", "0.0.0.0:8444"},

		// Explicit IPv6 wildcard same story as 0.0.0.0.
		{"explicit [::]:port unchanged", "[::]:8444", "[::]:8444"},

		// Explicit loopback host stays explicit. No-op.
		{"explicit 127.0.0.1:port unchanged", "127.0.0.1:8444", "127.0.0.1:8444"},
		{"explicit [::1]:port unchanged", "[::1]:8444", "[::1]:8444"},

		// LAN IP / management network — the operator explicitly said
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

// IEEE-097: EffectiveStorePath resolves a per-store on-disk path with the
// precedence rule:
//
//  1. If dedicatedPath is non-empty, return it verbatim (back-compat for
//     SEP2_SUBSCRIPTION_STORE_PATH etc.).
//  2. Else if DataDir is non-empty, return <DataDir>/<name>.json.
//  3. Else return "" — in-memory mode.
//
// Empty Config (no DataDir, no dedicated path) keeps the historical pre-
// IEEE-097 behavior so every existing test path stays unchanged.
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
			// IEEE-097 precedence regression: SEP2_SUBSCRIPTION_STORE_PATH
			// (the IEEE-077-era dedicated knob) must keep working even
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
