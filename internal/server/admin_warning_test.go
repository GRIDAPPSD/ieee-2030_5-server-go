package server

import (
	"strings"
	"testing"
)

// IEEE-137: when the admin listener is bound to a non-loopback address
// and the operator has not declared an upstream reverse proxy via
// SEP2_ADMIN_BEHIND_PROXY=true, the server logs a startup WARNING
// explaining that without an upstream proxy injecting X-Forwarded-For
// or Forwarded, AdminAuthMiddleware Path 0 will admit ALL traffic as
// loopback-local. nginx's stock config does NOT inject those headers,
// so a stock-nginx-fronted admin would silently bypass admin auth.
//
// adminProxyWarning is the pure helper exposing the gate. The test
// drives it directly so the assertion does not need to capture log
// output.
func TestAdminProxyWarning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		addr        string
		behindProxy bool
		wantWarn    bool // true = non-empty warning expected
	}{
		// Loopback bind (IEEE-136 default). No warning regardless of
		// the proxy hint — Path 0 admit-decline is moot when only the
		// host can reach the listener.
		{"loopback 127.0.0.1, no proxy hint → no warn", "127.0.0.1:8444", false, false},
		{"loopback 127.0.0.1, proxy hint set → no warn", "127.0.0.1:8444", true, false},
		{"loopback IPv6 [::1], no proxy hint → no warn", "[::1]:8444", false, false},

		// Non-loopback bind without proxy hint — fire the warning.
		// This is the structural fix IEEE-137 codifies.
		{"public 0.0.0.0, no proxy hint → WARN", "0.0.0.0:8444", false, true},
		{"public IPv6 [::], no proxy hint → WARN", "[::]:8444", false, true},
		{"LAN IP 192.168.1.5, no proxy hint → WARN", "192.168.1.5:8444", false, true},

		// Non-loopback bind WITH proxy hint — silence the warning.
		// Operator has acknowledged the requirement.
		{"public 0.0.0.0, proxy hint set → no warn", "0.0.0.0:8444", true, false},
		{"LAN IP, proxy hint set → no warn", "192.168.1.5:8444", true, false},

		// Empty addr means admin disabled entirely. Caller gates this
		// case before reaching us, but assert no warning anyway.
		{"empty addr → no warn", "", false, false},
		{"empty addr, proxy hint set → no warn", "", true, false},

		// Hostname that we cannot resolve safely — treat as non-loopback
		// (false positive warning is harmless; false negative would let
		// a misconfigured deploy ship silently). Operator can always set
		// the proxy hint to silence.
		{"hostname unresolved → WARN (conservative)", "admin.internal:8444", false, true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := adminProxyWarning(tc.addr, tc.behindProxy)
			if tc.wantWarn && got == "" {
				t.Errorf("adminProxyWarning(%q, %v): expected warning, got empty", tc.addr, tc.behindProxy)
			}
			if !tc.wantWarn && got != "" {
				t.Errorf("adminProxyWarning(%q, %v): expected empty, got %q", tc.addr, tc.behindProxy, got)
			}
			if tc.wantWarn {
				// Pin the load-bearing content fragments so a future
				// edit cannot silently soften the warning past
				// recognition. The operator MUST see "WARNING",
				// "X-Forwarded-For", and the env var name to act on
				// it. The bind address must be echoed so the operator
				// knows which listener tripped the warning.
				for _, want := range []string{
					"WARNING",
					"X-Forwarded-For",
					"SEP2_ADMIN_BEHIND_PROXY",
					tc.addr,
				} {
					if !strings.Contains(got, want) {
						t.Errorf("warning missing fragment %q\n---warning---\n%s", want, got)
					}
				}
			}
		})
	}
}

// TestIsLoopbackBind covers the helper independently of the warning
// gate so a regression in IPv6 / IP-vs-hostname handling lands as a
// targeted failure rather than a less informative end-to-end miss.
func TestIsLoopbackBind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8444", true},
		{"127.0.0.5:8444", true}, // entire 127.0.0.0/8 is loopback
		{"[::1]:8444", true},
		// HIGH-3 (Tess): IPv4-in-IPv6 mapped form parses as loopback.
		// net.ParseIP("::ffff:127.0.0.1") returns a non-nil IP whose
		// IsLoopback() is true; pin that so a future net-package or
		// helper change cannot silently bypass the warning gate on
		// dual-stack listeners.
		{"[::ffff:127.0.0.1]:8444", true},
		{"0.0.0.0:8444", false},
		{"[::]:8444", false},
		{"192.168.1.5:8444", false},
		{"10.0.0.1:8444", false},
		{"admin.internal:8444", false}, // hostname not resolved
		{"", false},                    // empty
		{":8444", false},               // bare port; caller resolves first
		{"not-a-valid-addr", false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.addr, func(t *testing.T) {
			t.Parallel()
			if got := isLoopbackBind(tc.addr); got != tc.want {
				t.Errorf("isLoopbackBind(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}
