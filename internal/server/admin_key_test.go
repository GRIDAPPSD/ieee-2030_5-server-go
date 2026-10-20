package server

import (
	"strconv"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
)

// TestValidateAdminKey pins the key rules on every bind: a key must be
// present, carry credential material, and be at least 16 characters.
func TestValidateAdminKey(t *testing.T) {
	t.Parallel()

	const sixteen = "0123456789abcdef"
	tests := []struct {
		name    string
		key     string
		wantErr string // substring the refusal must carry; empty = accepted
	}{
		{"unset is refused", "", "SEP2_ADMIN_SECRET_FILE"},
		{"single space", " ", "whitespace only"},
		{"tab", "\t", "whitespace only"},
		{"newline", "\n", "whitespace only"},
		{"carriage return", "\r", "whitespace only"},
		{"mixed whitespace", " \t \n ", "whitespace only"},
		{"short ordinary token", "s3cret", "16"},
		{"fifteen characters", sixteen[:15], "16"},
		{"sixteen characters", sixteen, ""},
		{"sixteen multibyte characters", strings.Repeat("\u00e9", 16), ""},
		{"fifteen multibyte characters", strings.Repeat("\u00e9", 15), "16"},
		{"trailing space counts as credential material", "s3cret-with-space ", ""},
		{"leading space counts as credential material", " s3cret-with-space", ""},
		{"generated 64 hex", strings.Repeat("ab", 32), ""},
	}

	if got, want := len(tests), 14; got != want {
		t.Fatalf("admin-key table has %d cases, want %d", got, want)
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateAdminKey(tc.key)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateAdminKey(%q) = %v, want nil", tc.key, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateAdminKey(%q) = nil, want a refusal", tc.key)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("refusal does not carry %q\n---error---\n%v", tc.wantErr, err)
			}
			// The refusal must not echo the configured value, so a startup
			// failure cannot be a route by which a credential is logged. The
			// check is against the quoted form: a bare-substring check on a
			// whitespace-only value matches any prose that contains a space.
			if tc.key != "" {
				if quoted := strconv.Quote(tc.key); strings.Contains(err.Error(), quoted) {
					t.Errorf("refusal echoes the configured value as %s\n---error---\n%v", quoted, err)
				}
			}
		})
	}
}

func TestAdminSignInHint(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  config.Config
		addr string
		want string
	}{
		{
			"file source, loopback, plain http",
			config.Config{AdminKeySource: "/home/u/tls/admin.secret"},
			"127.0.0.1:8444",
			"admin sign-in: http://127.0.0.1:8444/login, key in /home/u/tls/admin.secret (print it with: sep2server admin-key)",
		},
		{
			"file source generated now",
			config.Config{AdminKeySource: "/home/u/tls/admin.secret", AdminKeyGenerated: true},
			"127.0.0.1:8444",
			"admin sign-in: http://127.0.0.1:8444/login, key in /home/u/tls/admin.secret (generated now) (print it with: sep2server admin-key)",
		},
		{
			"env source over https",
			config.Config{AdminKeySource: "SEP2_ADMIN_KEY", AdminTLS: true},
			"127.0.0.1:9443",
			"admin sign-in: https://127.0.0.1:9443/login, key from SEP2_ADMIN_KEY",
		},
		{
			"non-loopback bind uses the bind host",
			config.Config{AdminKeySource: "SEP2_ADMIN_KEY"},
			"192.0.2.7:8444",
			"admin sign-in: http://192.0.2.7:8444/login, key from SEP2_ADMIN_KEY",
		},
		{
			"ipv6 loopback",
			config.Config{AdminKeySource: "SEP2_ADMIN_KEY"},
			"[::1]:8444",
			"admin sign-in: http://127.0.0.1:8444/login, key from SEP2_ADMIN_KEY",
		},
		{
			"key set directly with no recorded source",
			config.Config{AdminKey: "k"},
			"127.0.0.1:8444",
			"admin sign-in: http://127.0.0.1:8444/login, key from the configuration",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := adminSignInHint(&tc.cfg, tc.addr); got != tc.want {
				t.Errorf("hint = %q\nwant   %q", got, tc.want)
			}
		})
	}
}
