package config

import (
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
)

// The floor follows the issuer's longest control, so a longer control raises
// it rather than leaving a window the readings no longer cover.
func TestMinMirrorReadingRetention_IsLongestControlPlusHold(t *testing.T) {
	t.Parallel()
	if want := dercontrol.DefaultMaxDuration + 900*time.Second; MinMirrorReadingRetention != want {
		t.Fatalf("MinMirrorReadingRetention = %v, want %v", MinMirrorReadingRetention, want)
	}
	if MinMirrorReadingRetention != 87300*time.Second {
		t.Fatalf("MinMirrorReadingRetention = %v, want 87300 s", MinMirrorReadingRetention)
	}
}

func TestEffectiveMirrorReadingRetention(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      time.Duration
		want    time.Duration
		wantErr bool
	}{
		{0, 90000 * time.Second, false},
		{87300 * time.Second, 87300 * time.Second, false},
		{2592000 * time.Second, 2592000 * time.Second, false},
		{87299 * time.Second, 0, true},
		{2592001 * time.Second, 0, true},
		{87300*time.Second + time.Millisecond, 0, true},
		{-time.Second, 0, true},
	}
	for _, tc := range tests {
		c := &Config{MirrorReadingRetention: tc.in}
		got, err := c.EffectiveMirrorReadingRetention()
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("EffectiveMirrorReadingRetention(%v) = %v, %v; want %v, err %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

// The cap's floor is one reading every 300 s across the retention floor, ends
// included, so the cap never removes what the time floor keeps at that rate.
func TestMinMirrorReadingMaxPerSeries_HoldsCadenceAcrossFloor(t *testing.T) {
	t.Parallel()
	if MinMirrorReadingMaxPerSeries != 87300/300+1 {
		t.Fatalf("MinMirrorReadingMaxPerSeries = %d, want %d", MinMirrorReadingMaxPerSeries, 87300/300+1)
	}
}

func TestEffectiveMirrorReadingMaxPerSeries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, want int
		wantErr  bool
	}{{0, 20000, false}, {292, 292, false}, {2592000, 2592000, false}, {291, 0, true}, {1, 0, true}, {2592001, 0, true}, {-1, 0, true}} {
		c := &Config{MirrorReadingMaxPerSeries: tc.in}
		got, err := c.EffectiveMirrorReadingMaxPerSeries()
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("EffectiveMirrorReadingMaxPerSeries(%d) = %d, %v; want %d, err %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

// A cap that cannot hold a 300 s cadence across the time floor, or one above
// the ceiling, is refused.
func TestParseMirrorReadingMaxPerSeries_Bounds(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"1", "291", "2592001"} {
		if _, err := ParseMirrorReadingMaxPerSeries(v); err == nil {
			t.Errorf("cap %s accepted, want refused", v)
		}
	}
	for _, v := range []string{"292", "2592000"} {
		if _, err := ParseMirrorReadingMaxPerSeries(v); err != nil {
			t.Errorf("cap %s refused: %v", v, err)
		}
	}
}
