package config

import (
	"testing"
	"time"
)

func TestParseFlowReservationDeadlineSeconds(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"1", time.Second, false},
		{"300", 300 * time.Second, false},
		{"3600", time.Hour, false},
		{"0", 0, true},
		{"3601", 0, true},
		{"-5", 0, true},
		{"1.5", 0, true},
		{"abc", 0, true},
		{"99999999999999999999", 0, true},
	}
	for _, tc := range tests {
		got, err := ParseFlowReservationDeadlineSeconds(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("ParseFlowReservationDeadlineSeconds(%q) = %v, %v; want %v, error=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestEffectiveFlowReservationDeadline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		set     time.Duration
		want    time.Duration
		wantErr bool
	}{
		{"unset takes 300 s", 0, 300 * time.Second, false},
		{"configured value kept", 90 * time.Second, 90 * time.Second, false},
		{"lower bound", time.Second, time.Second, false},
		{"upper bound", time.Hour, time.Hour, false},
		{"below the lower bound", time.Second - 1, 0, true},
		{"above the upper bound", time.Hour + 1, 0, true},
		{"negative", -time.Second, 0, true},
	}
	for _, tc := range tests {
		got, err := (&Config{FlowReservationDeadline: tc.set}).EffectiveFlowReservationDeadline()
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s: got %v, %v; want %v, error=%v", tc.name, got, err, tc.want, tc.wantErr)
		}
	}
}
