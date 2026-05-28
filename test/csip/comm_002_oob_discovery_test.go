// Package csip_test holds the IEEE-2030.5 CSIP V1.2 conformance harness.
// Tests in this package each map to one V1.2 procedure ID (COMM-NNN,
// CORE-NNN, BASIC-NNN) with the procedure section cited in the test's
// doc comment. The harness boots an in-process spec server per test
// via test/csip/csiptest.BootServer.
//
// CSIP V1.2 §5.2 — Out-of-Band Discovery. Asserts /dcap GET succeeds
// over mTLS and returns a conformant DeviceCapability with required
// links populated.
//
// This test is the named CSIP conformance counterpart to the older
// SunSpec V1.2 PKI smoke (handshake_test.go). Where handshake_test.go
// drives the server with the external SunSpec test PKI fixtures
// (CCM-8 path, fixtures gated behind env vars + t.Skip), this test
// runs unconditionally against an ephemeral PKI booted by
// csiptest.BootServer. It is the test that satisfies the IEEE-067
// COMM-002 line item in the Phase 3 V1.2 coverage matrix.
//
// Procedure (V1.2 §5.2 — Out-of-Band Discovery):
//  1. Client has the server's base URL provisioned out-of-band.
//  2. Client issues GET <base>/dcap over mTLS.
//  3. Server returns 200 OK with a DeviceCapability resource.
//  4. DeviceCapability advertises the function-set links the server
//     supports. CSIP V1.2 requires the entry-point links a server
//     must populate to participate in a conformant deployment.
//
// Required links asserted here (the CSIP-minimum baseline that lets a
// V1.2 client walk the resource graph):
//   - EndDeviceListLink — entry to per-device function sets.
//   - TimeLink          — required for time-quality validation
//     (chained-GET pattern, see CORE-005).
//   - SelfDeviceLink    — server's own identity surface (required
//     once IEEE-001 lands SFDI/LFDI in GCM mode).
//
// We use t.Errorf rather than t.Fatalf at each link so that a single
// run surfaces every gap; a future server regression that drops two
// links should fail two assertions, not just the first.
package csip_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// TestCOMM_002_OOBDiscovery proves the CSIP V1.2 §5.2 Out-of-Band
// Discovery contract end-to-end against a fresh BootServer().
func TestCOMM_002_OOBDiscovery(t *testing.T) {
	t.Parallel()

	srv := csiptest.BootServer(t)

	dcap, err := srv.Client().GetDeviceCapability(context.Background())
	if err != nil {
		t.Fatalf("GetDeviceCapability: %v", err)
	}

	// Step 3: /dcap self-Href roundtrip. A server that omits this
	// breaks every chained-GET client that uses it as the walk root.
	if dcap.Href != "/dcap" {
		t.Errorf("DeviceCapability.Href = %q, want %q", dcap.Href, "/dcap")
	}

	// Step 4: required-link minimum. Each assertion is independent so
	// a regression that drops more than one shows up in one run.
	if dcap.EndDeviceListLink == nil || dcap.EndDeviceListLink.Href == "" {
		t.Errorf("EndDeviceListLink.Href is empty; CSIP V1.2 §5.2 requires it for per-device function-set walking")
	}
	if dcap.TimeLink == nil || dcap.TimeLink.Href == "" {
		t.Errorf("TimeLink.Href is empty; CSIP V1.2 §5.2 requires it (paired with CORE-005 time-quality assertion)")
	}
	if dcap.SelfDeviceLink == nil || dcap.SelfDeviceLink.Href == "" {
		t.Errorf("SelfDeviceLink.Href is empty; CSIP V1.2 §5.2 requires it for server-identity discovery")
	}
}
