package server

import (
	"strconv"
	"strings"
	"testing"
)

// TestValidateAdminKey pins the three-way split. Unset is the deliberate
// "Bearer auth disabled" state; whitespace-only is a typo and a hard startup
// error; anything with credential material is accepted verbatim, including a
// value whose own whitespace is part of the secret.
func TestValidateAdminKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"unset disables Bearer deliberately", "", false},
		{"single space", " ", true},
		{"tab", "\t", true},
		{"newline", "\n", true},
		{"carriage return", "\r", true},
		{"mixed whitespace", " \t \n ", true},
		{"ordinary token", "s3cret", false},
		{"token with a trailing space is credential material", "s3cret ", false},
		{"token with a leading space is credential material", " s3cret", false},
		{"a single non-space character", "x", false},
	}

	if got, want := len(tests), 10; got != want {
		t.Fatalf("admin-key table has %d cases, want %d", got, want)
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateAdminKey(tc.key)
			if tc.wantErr && err == nil {
				t.Fatalf("validateAdminKey(%q) = nil, want a refusal", tc.key)
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("validateAdminKey(%q) = %v, want nil", tc.key, err)
				}
				return
			}
			if !strings.Contains(err.Error(), "SEP2_ADMIN_KEY") {
				t.Errorf("refusal does not name the env var\n---error---\n%v", err)
			}
			// The refusal must not echo the configured value, so a startup
			// failure cannot be a route by which a credential is logged. The
			// check is against the quoted form: a bare-substring check on a
			// whitespace-only value matches any prose that contains a space.
			if quoted := strconv.Quote(tc.key); strings.Contains(err.Error(), quoted) {
				t.Errorf("refusal echoes the configured value as %s\n---error---\n%v", quoted, err)
			}
		})
	}
}
