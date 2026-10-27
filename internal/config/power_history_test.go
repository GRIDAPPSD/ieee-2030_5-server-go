package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestEffectivePowerHistoryRetention(t *testing.T) {
	for _, c := range []struct {
		hours   int
		want    time.Duration
		wantErr bool
	}{
		{0, 48 * time.Hour, false},
		{1, time.Hour, false},
		{720, 720 * time.Hour, false},
		{-1, 0, true},
		{721, 0, true},
	} {
		got, err := (&Config{PowerHistoryHours: c.hours}).EffectivePowerHistoryRetention()
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("hours %d: got %v, %v; want %v, error %v", c.hours, got, err, c.want, c.wantErr)
		}
	}
}

// The history is kept only under a data directory.
func TestEffectivePowerHistoryDir(t *testing.T) {
	if got := (&Config{}).EffectivePowerHistoryDir(); got != "" {
		t.Errorf("no DataDir: dir = %q, want none", got)
	}
	if got, want := (&Config{DataDir: "/var/lib/sep2"}).EffectivePowerHistoryDir(), filepath.Join("/var/lib/sep2", "power-history"); got != want {
		t.Errorf("dir = %q, want %q", got, want)
	}
}

func TestParsePowerHistoryHours(t *testing.T) {
	if n, err := ParsePowerHistoryHours(""); n != 0 || err != nil {
		t.Errorf("empty = %d, %v, want unset", n, err)
	}
	if n, err := ParsePowerHistoryHours("72"); n != 72 || err != nil {
		t.Errorf("72 = %d, %v", n, err)
	}
	for _, bad := range []string{"0", "721", "x", "1.5", " 5"} {
		if n, err := ParsePowerHistoryHours(bad); err == nil {
			t.Errorf("%q = %d, want an error", bad, n)
		}
	}
}
