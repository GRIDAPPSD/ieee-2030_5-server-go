package main

import (
	"testing"
)

// TestEdevIDFromLocation exercises the Location-header parser. The server
// returns "Location: /edev/{id}" on a successful POST /edev; the setup
// binary must extract the bare ID string from that path so the subscribe
// mode can form "/edev/{id}/sub" URLs without re-registering.
func TestEdevIDFromLocation(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "canonical form with leading slash",
			input: "/edev/42",
			want:  "42",
		},
		{
			name:  "no leading slash",
			input: "edev/99",
			want:  "99",
		},
		{
			name:  "alphanumeric edev ID",
			input: "/edev/abc-123",
			want:  "abc-123",
		},
		{
			name:  "trailing path component is ignored",
			input: "/edev/7/sub",
			want:  "7",
		},
		{
			name:  "empty location returns empty string",
			input: "",
			want:  "",
		},
		{
			name:  "wrong root segment returns empty string",
			input: "/foo/42",
			want:  "",
		},
		{
			name:  "edev with empty ID returns empty string",
			input: "/edev/",
			want:  "",
		},
		{
			name:  "just slash returns empty string",
			input: "/",
			want:  "",
		},
		{
			// Absolute-form Location (RFC 7231 compliant server that returns
			// an absolute URI). Previously parsed to "" because the scheme+host
			// prefix caused the path split to fail.
			name:  "absolute-form location with host",
			input: "https://127.0.0.1:8443/edev/42",
			want:  "42",
		},
		{
			name:  "absolute-form with trailing path component",
			input: "https://127.0.0.1:8443/edev/99/sub",
			want:  "99",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := edevIDFromLocation(tc.input)
			if got != tc.want {
				t.Errorf("edevIDFromLocation(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
