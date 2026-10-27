package config_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// #1156: SEP2_DERMS_MAX_DEVICE_W.
func TestParseDERMSMaxDeviceW(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"", 0, true},
		{"1", 1, true},
		{"250000", 250000, true},
		{"1000000000000", 1000000000000, true},
		{"0", 0, false},
		{"-5", 0, false},
		{"1000000000001", 0, false},
		{"1e6", 0, false},
	} {
		got, err := config.ParseDERMSMaxDeviceW(tc.in)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("ParseDERMSMaxDeviceW(%q) = %d, %v; want %d, ok %v", tc.in, got, err, tc.want, tc.ok)
		}
	}
}

func TestEffectiveDERMSMaxDeviceW(t *testing.T) {
	if got, err := (&config.Config{}).EffectiveDERMSMaxDeviceW(); err != nil || got != 1000000 {
		t.Errorf("unset = %d, %v; want the 1000000 W default", got, err)
	}
	if got, err := (&config.Config{DERMSMaxDeviceW: 5000}).EffectiveDERMSMaxDeviceW(); err != nil || got != 5000 {
		t.Errorf("5000 = %d, %v; want 5000", got, err)
	}
	if _, err := (&config.Config{DERMSMaxDeviceW: -1}).EffectiveDERMSMaxDeviceW(); err == nil {
		t.Error("-1 accepted")
	}
}
