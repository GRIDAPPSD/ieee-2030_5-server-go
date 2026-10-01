package config

import (
	"testing"
	"time"
)

func TestParseFlowReservationRetentionGraceSeconds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"1", time.Second, false},
		{"1800", 1800 * time.Second, false},
		{"604800", 7 * 24 * time.Hour, false},
		{"0", 0, true},
		{"604801", 0, true},
		{"-5", 0, true},
		{"1.5", 0, true},
		{"abc", 0, true},
		{"99999999999999999999", 0, true},
	}
	for _, tc := range tests {
		got, err := ParseFlowReservationRetentionGraceSeconds(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseFlowReservationRetentionGraceSeconds(%q) = %v, %v; want %v, error=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestEffectiveFlowReservationRetentionGrace(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		set     time.Duration
		want    time.Duration
		wantErr bool
	}{
		{"unset takes 1800 s", 0, 1800 * time.Second, false},
		{"configured value kept", 90 * time.Second, 90 * time.Second, false},
		{"lower bound", time.Second, time.Second, false},
		{"upper bound", 7 * 24 * time.Hour, 7 * 24 * time.Hour, false},
		{"below the lower bound", time.Second - 1, 0, true},
		{"above the upper bound", 7*24*time.Hour + time.Second, 0, true},
		{"not whole seconds", 1500 * time.Millisecond, 0, true},
		{"negative", -time.Second, 0, true},
	}
	for _, tc := range tests {
		got, err := (&Config{FlowReservationRetentionGrace: tc.set}).EffectiveFlowReservationRetentionGrace()
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v, error=%v", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}
