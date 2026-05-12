// Tests for the BootServer helper. These exercise the spec server in
// GCM mode (the default — fast, no fixture deps). Tests that exercise
// the CCM-8 path live in test/csip/handshake_test.go, which is the
// canonical CCM consumer.
package csiptest_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// TestBootServer_DeviceCapabilitySucceeds boots a server and confirms
// the returned Client can fetch /dcap end-to-end. This is the smoke
// test from the ticket: "BootServer returns a working base URL."
func TestBootServer_DeviceCapabilitySucceeds(t *testing.T) {
	t.Parallel()

	srv := csiptest.BootServer(t)

	dcap, err := srv.Client().GetDeviceCapability(context.Background())
	if err != nil {
		t.Fatalf("GetDeviceCapability: %v", err)
	}
	if dcap.Href != "/dcap" {
		t.Errorf("dcap.Href = %q, want /dcap", dcap.Href)
	}
	if srv.BaseURL == "" {
		t.Error("BaseURL is empty")
	}
	if srv.Addr() == "" {
		t.Error("Addr is empty")
	}
}

// TestBootServer_ParallelDistinctPorts boots two servers in parallel
// subtests and asserts they end up on different ports. Random-port
// allocation is the hard constraint from the ticket: state leak
// between parallel tests = weeks of Phase 3 flakes.
func TestBootServer_ParallelDistinctPorts(t *testing.T) {
	t.Parallel()

	// Run two parallel subtests that each boot a server, hand the
	// observed Addr back via a channel, and join. If the channel
	// returns the same Addr twice, random-port allocation broke.
	addrCh := make(chan string, 2)

	t.Run("first", func(t *testing.T) {
		t.Parallel()
		srv := csiptest.BootServer(t)
		if _, err := srv.Client().GetDeviceCapability(context.Background()); err != nil {
			t.Fatalf("first: GetDeviceCapability: %v", err)
		}
		addrCh <- srv.Addr()
	})

	t.Run("second", func(t *testing.T) {
		t.Parallel()
		srv := csiptest.BootServer(t)
		if _, err := srv.Client().GetDeviceCapability(context.Background()); err != nil {
			t.Fatalf("second: GetDeviceCapability: %v", err)
		}
		addrCh <- srv.Addr()
	})

	t.Cleanup(func() {
		// Drain the channel after the parallel subtests complete so
		// the cleanup-time assertion has both values. Subtests run
		// concurrently with this outer test's body, but Cleanup runs
		// after they finish — guaranteed by testing.T semantics.
		close(addrCh)
		seen := map[string]bool{}
		for addr := range addrCh {
			if seen[addr] {
				t.Errorf("both subtests booted on the same Addr %q (random-port allocation regressed)", addr)
			}
			seen[addr] = true
		}
		if len(seen) != 2 {
			t.Errorf("expected 2 distinct addrs, got %d (%v)", len(seen), seen)
		}
	})
}

// TestBootServer_CleanupShutsDown boots a server, captures its Addr,
// runs the t.Cleanup chain explicitly via a subtest scope, and
// confirms a post-cleanup Dial against the captured Addr fails. This
// proves the cleanup function actually closes the listener (not just
// that t.Cleanup got registered).
func TestBootServer_CleanupShutsDown(t *testing.T) {
	t.Parallel()

	var capturedAddr string

	t.Run("scope", func(t *testing.T) {
		srv := csiptest.BootServer(t)
		capturedAddr = srv.Addr()

		// Sanity: the server is alive while we're inside this scope.
		if _, err := srv.Client().GetDeviceCapability(context.Background()); err != nil {
			t.Fatalf("server unreachable inside scope: %v", err)
		}
	})

	// "scope" has now exited and its t.Cleanup has run — meaning
	// BootServer's t.Cleanup closed the http.Server and the listener.
	// Dialing the captured Addr must now fail.
	conn, err := net.DialTimeout("tcp", capturedAddr, 500*time.Millisecond)
	if err == nil {
		_ = conn.Close()
		t.Fatalf("Dial succeeded after cleanup; server did not shut down (addr=%s)", capturedAddr)
	}
}
