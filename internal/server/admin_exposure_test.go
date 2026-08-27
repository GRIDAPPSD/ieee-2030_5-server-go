package server

import (
	"strings"
	"testing"
)

// TestValidateAdminExposure covers the gate that refuses an admin bind
// reachable from outside this host. The boundary is itself a candidate for the
// condition it rejects: the unspecified addresses bind every interface, and an
// address the helper cannot positively establish as loopback is treated as
// exposed rather than assumed safe.
func TestValidateAdminExposure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		addr             string
		allowNonLoopback bool
		wantErr          bool
	}{
		{"loopback IPv4", "127.0.0.1:8444", false, false},
		{"loopback elsewhere in 127.0.0.0/8", "127.0.0.5:8444", false, false},
		{"loopback IPv6", "[::1]:8444", false, false},
		{"IPv4-mapped loopback", "[::ffff:127.0.0.1]:8444", false, false},

		// The unspecified addresses bind every interface, so they are the
		// exposure case even though they name no routable host.
		{"unspecified IPv4 0.0.0.0", "0.0.0.0:8444", false, true},
		{"unspecified IPv6 [::]", "[::]:8444", false, true},

		{"routable LAN address", "192.168.1.5:8444", false, true},
		{"routable private range", "10.0.0.1:8444", false, true},

		// Not positively loopback, so refused. A name could resolve to
		// loopback but we will not gamble on DNS to find out.
		{"unresolved hostname", "admin.internal:8444", false, true},
		{"host-less bare port", ":8444", false, true},
		{"malformed address", "not-a-valid-addr", false, true},

		// Empty means the admin listener is disabled; the caller gates it.
		{"empty addr", "", false, false},

		// The opt-in admits every exposed form, and changes nothing for the
		// forms that were already admitted.
		{"unspecified IPv4 with opt-in", "0.0.0.0:8444", true, false},
		{"unspecified IPv6 with opt-in", "[::]:8444", true, false},
		{"routable LAN address with opt-in", "192.168.1.5:8444", true, false},
		{"unresolved hostname with opt-in", "admin.internal:8444", true, false},
		{"loopback with opt-in", "127.0.0.1:8444", true, false},
	}

	if got, want := len(tests), 17; got != want {
		t.Fatalf("exposure table has %d cases, want %d", got, want)
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateAdminExposure(tc.addr, tc.allowNonLoopback)
			if tc.wantErr && err == nil {
				t.Fatalf("validateAdminExposure(%q, %v) = nil, want a refusal", tc.addr, tc.allowNonLoopback)
			}
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("validateAdminExposure(%q, %v) = %v, want nil", tc.addr, tc.allowNonLoopback, err)
				}
				return
			}
			// The operator must be able to act on the refusal in one step,
			// so it names the opt-in and echoes the address that tripped it.
			for _, want := range []string{"SEP2_ADMIN_ALLOW_NON_LOOPBACK", tc.addr} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal missing %q\n---error---\n%v", want, err)
				}
			}
		})
	}
}
