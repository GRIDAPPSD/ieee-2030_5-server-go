package server

import (
	"strings"
	"testing"
)

// TestAdminSecureCookieWarning covers the gate. The warning fires only where
// the browser login flow genuinely cannot complete: plain HTTP, a non-loopback
// bind, and no declared upstream TLS proxy. Every other combination is silent,
// because a warning that fires on a working configuration gets ignored on the
// broken one.
func TestAdminSecureCookieWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		addr        string
		adminTLS    bool
		behindProxy bool
		wantWarn    bool
	}{
		// The one broken configuration.
		{"plain HTTP, non-loopback, no proxy", "192.168.1.5:8444", false, false, true},
		{"plain HTTP, unspecified IPv4, no proxy", "0.0.0.0:8444", false, false, true},
		{"plain HTTP, unspecified IPv6, no proxy", "[::]:8444", false, false, true},
		{"plain HTTP, unresolved hostname, no proxy", "admin.internal:8444", false, false, true},

		// Loopback keeps the cookie: a browser treats it as a
		// potentially-trustworthy origin.
		{"plain HTTP on loopback IPv4", "127.0.0.1:8444", false, false, false},
		{"plain HTTP on loopback IPv6", "[::1]:8444", false, false, false},
		{"plain HTTP on IPv4-mapped loopback", "[::ffff:127.0.0.1]:8444", false, false, false},

		// HTTPS delivers the cookie over TLS, so there is nothing to warn about.
		{"HTTPS on non-loopback", "192.168.1.5:8444", true, false, false},
		{"HTTPS on loopback", "127.0.0.1:8444", true, false, false},

		// A declared upstream proxy terminates TLS at the browser-facing
		// origin: the supported Caddy-mode deployment.
		{"plain HTTP behind a declared proxy", "192.168.1.5:8444", false, true, false},
		{"HTTPS behind a declared proxy", "192.168.1.5:8444", true, true, false},

		// Admin disabled entirely.
		{"empty addr", "", false, false, false},
	}

	if got, want := len(tests), 12; got != want {
		t.Fatalf("secure-cookie table has %d cases, want %d", got, want)
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := adminSecureCookieWarning(tc.addr, tc.adminTLS, tc.behindProxy)
			if tc.wantWarn && got == "" {
				t.Fatalf("adminSecureCookieWarning(%q, tls=%v, proxy=%v): expected a warning, got empty",
					tc.addr, tc.adminTLS, tc.behindProxy)
			}
			if !tc.wantWarn {
				if got != "" {
					t.Fatalf("adminSecureCookieWarning(%q, tls=%v, proxy=%v): expected empty, got %q",
						tc.addr, tc.adminTLS, tc.behindProxy, got)
				}
				return
			}
			// The operator must be able to read the consequence and the two
			// ways out of it, and know which listener tripped the warning.
			for _, want := range []string{
				"WARNING",
				"Secure",
				"admin_ticket",
				"CANNOT complete",
				"SEP2_ADMIN_TLS=true",
				"SEP2_ADMIN_BEHIND_PROXY=true",
				tc.addr,
			} {
				if !strings.Contains(got, want) {
					t.Errorf("warning missing fragment %q\n---warning---\n%s", want, got)
				}
			}
		})
	}
}
