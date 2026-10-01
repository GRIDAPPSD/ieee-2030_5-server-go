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

func TestEffectiveMirrorReadingMaxPerMirror(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		in, want int
		wantErr  bool
	}{{0, 20000, false}, {1, 1, false}, {-1, 0, true}} {
		c := &Config{MirrorReadingMaxPerMirror: tc.in}
		got, err := c.EffectiveMirrorReadingMaxPerMirror()
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("EffectiveMirrorReadingMaxPerMirror(%d) = %d, %v; want %d, err %v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}
