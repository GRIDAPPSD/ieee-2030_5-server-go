package server

import (
	"net/http"
	"testing"
	"time"
)

// TestProtocolServerTimeoutsSet asserts that the protocol server (SEP2 wire)
// has all four timeout fields set to non-zero values. A zero timeout means
// "no limit" — infinite — which is a Slowloris / slow-body attack surface.
func TestProtocolServerTimeoutsSet(t *testing.T) {
	srv := newProtocolServer(nil)
	assertServerTimeouts(t, "protocolSrv", srv)
}

// TestAdminServerTimeoutsSet asserts that the admin server has all four
// timeout fields set to non-zero values.
func TestAdminServerTimeoutsSet(t *testing.T) {
	srv := newAdminServer(nil)
	assertServerTimeouts(t, "adminSrv", srv)
}

// assertServerTimeouts checks that ReadHeaderTimeout, ReadTimeout,
// WriteTimeout, and IdleTimeout are all non-zero.
func assertServerTimeouts(t *testing.T, name string, srv *http.Server) {
	t.Helper()
	if srv.ReadHeaderTimeout == 0 {
		t.Errorf("%s: ReadHeaderTimeout is 0 (no limit)", name)
	}
	if srv.ReadTimeout == 0 {
		t.Errorf("%s: ReadTimeout is 0 (no limit)", name)
	}
	if srv.WriteTimeout == 0 {
		t.Errorf("%s: WriteTimeout is 0 (no limit)", name)
	}
	if srv.IdleTimeout == 0 {
		t.Errorf("%s: IdleTimeout is 0 (no limit)", name)
	}
}

// TestServerTimeoutConstants asserts that the named timeout constants are
// reasonable (positive, not absurdly small).
func TestServerTimeoutConstants(t *testing.T) {
	if serverReadHeaderTimeout <= 0 {
		t.Errorf("serverReadHeaderTimeout must be positive, got %v", serverReadHeaderTimeout)
	}
	if serverReadTimeout <= 0 {
		t.Errorf("serverReadTimeout must be positive, got %v", serverReadTimeout)
	}
	if serverWriteTimeout <= 0 {
		t.Errorf("serverWriteTimeout must be positive, got %v", serverWriteTimeout)
	}
	if serverIdleTimeout <= 0 {
		t.Errorf("serverIdleTimeout must be positive, got %v", serverIdleTimeout)
	}
	// Sanity: read header should be shorter than full read
	if serverReadHeaderTimeout >= serverReadTimeout {
		t.Errorf("serverReadHeaderTimeout (%v) should be < serverReadTimeout (%v)",
			serverReadHeaderTimeout, serverReadTimeout)
	}
	// Sanity: minimum 1 second to avoid absurdly tight values
	const minTimeout = time.Second
	for name, d := range map[string]time.Duration{
		"serverReadHeaderTimeout": serverReadHeaderTimeout,
		"serverReadTimeout":       serverReadTimeout,
		"serverWriteTimeout":      serverWriteTimeout,
		"serverIdleTimeout":       serverIdleTimeout,
	} {
		if d < minTimeout {
			t.Errorf("%s (%v) is below the 1-second minimum sanity floor", name, d)
		}
	}
}
