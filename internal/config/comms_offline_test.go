package config

import (
	"testing"
	"time"
)

func TestParseCommsOfflineAfterSeconds(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"1", time.Second, false},
		{"300", 300 * time.Second, false},
		{"86400", 24 * time.Hour, false},
		{"0", 0, true},
		{"-1", 0, true},
		{"86401", 0, true},
		{"five", 0, true},
		{"1.5", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseCommsOfflineAfterSeconds(tc.in)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("ParseCommsOfflineAfterSeconds(%q) = %v, %v; want %v, error=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestEffectiveCommsOfflineAfter(t *testing.T) {
	cases := []struct {
		name string
		set  time.Duration
		want time.Duration
	}{
		{"unset takes the default", 0, DefaultCommsOfflineAfter},
		{"a set value is kept", 90 * time.Second, 90 * time.Second},
		{"a negative value never comes back negative", -time.Second, DefaultCommsOfflineAfter},
	}
	for _, tc := range cases {
		if got := (&Config{CommsOfflineAfter: tc.set}).EffectiveCommsOfflineAfter(); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
