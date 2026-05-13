package config

import (
	"path/filepath"
	"testing"
)

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
