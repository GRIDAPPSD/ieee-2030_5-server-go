package config

import (
	"strings"
	"testing"
)

func TestParseDeviceLog(t *testing.T) {
	cases := []struct {
		in      string
		want    bool
		wantErr bool
	}{
		{"", true, false},
		{"true", true, false},
		{"false", false, false},
		{"FALSE", false, true},
		{"0", false, true},
		{"yes", false, true},
	}
	for _, tc := range cases {
		got, err := ParseDeviceLog(tc.in)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("ParseDeviceLog(%q) = %v, %v; want %v, error=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "SEP2_DEVICE_LOG") {
			t.Errorf("ParseDeviceLog(%q) error %q does not name the variable", tc.in, err)
		}
	}
}

func TestParseDeviceLogMaxBytes(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"1000000", 1_000_000, false},
		{"20000000", 20_000_000, false},
		{"999999", 0, true},
		{"20000001", 0, true},
		{"20", 0, true},
		{"-5", 0, true},
		{"20MB", 0, true},
		{"1.5e7", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseDeviceLogMaxBytes(tc.in)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("ParseDeviceLogMaxBytes(%q) = %v, %v; want %v, error=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "SEP2_DEVICE_LOG_MAX_BYTES") {
			t.Errorf("ParseDeviceLogMaxBytes(%q) error %q does not name the variable", tc.in, err)
		}
	}
}

func TestEffectiveDeviceLogMaxBytes(t *testing.T) {
	cases := []struct {
		set     int64
		want    int64
		wantErr bool
	}{
		{0, 20_000_000, false},
		{5_000_000, 5_000_000, false},
		{999_999, 0, true},
		{20_000_001, 0, true},
		{-1, 0, true},
	}
	for _, tc := range cases {
		got, err := (&Config{DeviceLogMaxBytes: tc.set}).EffectiveDeviceLogMaxBytes()
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("limit %d = %v, %v; want %v, error=%v", tc.set, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestEffectiveDeviceLogDir(t *testing.T) {
	dir, explicit := (&Config{}).EffectiveDeviceLogDir()
	if dir != "./etc/sep2/logs" || explicit {
		t.Errorf("unset = %q, %v; want ./etc/sep2/logs, false", dir, explicit)
	}
	dir, explicit = (&Config{DeviceLogDir: "/var/log/dev", DataDir: "/data"}).EffectiveDeviceLogDir()
	if dir != "/var/log/dev" || !explicit {
		t.Errorf("explicit = %q, %v; want /var/log/dev, true (DataDir must not move it)", dir, explicit)
	}
}
