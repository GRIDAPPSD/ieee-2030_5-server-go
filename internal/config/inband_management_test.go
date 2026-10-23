package config

import "testing"

func TestParseInBandManagement(t *testing.T) {
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
		{"off", false, true},
	}
	for _, tc := range cases {
		got, err := ParseInBandManagement(tc.in)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("ParseInBandManagement(%q) = %v, %v; want %v, error=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}
