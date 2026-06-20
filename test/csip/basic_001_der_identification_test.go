// CSIP V1.2 §8.1 — DER Identification. Tests EndDevice identity matches
// cert-derived SFDI/LFDI, and SelfDevice carries non-empty identity under
// both GCM and CCM (regression on IEEE-001 fix).
//
// V1.2 procedure step → assertion mapping (per V1.2 §8.1):
//
//	Step 1 (Boot server with a known device cert) ──────────► csiptest.BootServer with SunSpec V1.2 client cert
//	Step 2 (Client POSTs an EndDevice to /edev) ────────────► postEndDevice; assert 201 + Location header
//	Step 3 (GET the returned EndDevice; assert sFDI/lFDI) ──► assertEndDeviceIdentityFromCert
//	Step 4 (GET /sdev; assert non-empty sFDI/lFDI) ─────────► assertSelfDeviceIdentityNonEmpty
//	         under BOTH cipher modes — regression on IEEE-001
//
// V1.2 §3.2.3 specifies PIN = 111115 for the DER identification flow.
// The procedure here exercises identity binding via the client cert
// only; PIN handling lives in the Registration resource (separate
// ticket on the client side: IEEE-032 / IEEE-033). PIN value is
// documented for cross-reference, not asserted by BASIC-001.
//
// What this test relies on:
//   - csiptest.BootServer wiring serverSFDI/serverLFDI into NewRouter
//     from the booted server's leaf cert (IEEE-001 parity for the
//     in-process harness). Without that, /sdev returns empty identity
//     under both modes and Step 4 fails — which IS the regression this
//     test guards against.
//   - The SunSpec V1.2 test PKI under test/csip/fixtures/sunspec/.
//     Provisioned out of band per test/csip/README.md; skip cleanly
//     when fixtures are missing so fresh clones never fail.
//
// The CCM-mode subtest still drives the server-side gotls listener;
// the stdlib http.Client offers GCM ciphers and the gotls server
// accepts either GCM or CCM-8. Tightening to "negotiated CCM-8 only"
// is gated on IEEE-019/IEEE-020 (Phase 8 follow-up via IEEE-067).
package csip_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"os"
	"testing"

	sepTLS "gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2tls"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

// SunSpec V1.2 test cert (sanity check; helpers compute live):
//
//	SFDI = 273448359951
//	LFDI = 65DE1159BA8C8897D5A7F94997D22544EB90A2B7
//
// See test/csip/fixtures/single-edev.yaml — same values, computed
// offline by IEEE-057. The test below derives expected values
// from the cert at runtime via sepTLS.SFDI/LFDI rather than
// hardcoding, so a cert roll surfaces as a derived-value diff
// instead of a stale-constant assertion failure.

// expectedSunSpecSFDI is the SunSpec V1.2 test cert's SFDI per
// IEEE-057's single-edev.yaml fixture. Kept as a constant so the
// sanity check below catches a cert roll separately from a
// helper-logic regression.
const expectedSunSpecSFDI = "273448359951"

// expectedSunSpecLFDI is the SunSpec V1.2 test cert's LFDI per
// IEEE-057's single-edev.yaml fixture. Same rationale as the SFDI
// constant.
const expectedSunSpecLFDI = "65DE1159BA8C8897D5A7F94997D22544EB90A2B7"

// TestBASIC_001_DERIdentification implements CSIP V1.2 §8.1.
func TestBASIC_001_DERIdentification(t *testing.T) {
	t.Parallel()

	certPath, keyPath, rootsPath, ok := resolveFixtures()
	if !ok {
		t.Skip("CSIP fixtures not provisioned; see test/csip/README.md")
	}

	clientCert := loadSunSpecCert(t, certPath, keyPath)

	// Derive expected identity from the cert. The test asserts against
	// these so the procedure does not depend on hardcoded values beyond
	// the sanity-check constants above.
	leaf, err := x509.ParseCertificate(clientCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse SunSpec leaf: %v", err)
	}
	wantSFDI := sepTLS.SFDI(leaf)
	wantLFDI := sepTLS.LFDI(leaf)

	if wantSFDI != expectedSunSpecSFDI {
		t.Fatalf("sanity: SunSpec cert SFDI = %q, want %q (cert may have rolled; update constant + fixture)", wantSFDI, expectedSunSpecSFDI)
	}
	if wantLFDI != expectedSunSpecLFDI {
		t.Fatalf("sanity: SunSpec cert LFDI = %q, want %q (cert may have rolled; update constant + fixture)", wantLFDI, expectedSunSpecLFDI)
	}

	cases := []struct {
		name string
		opts []csiptest.BootOption
	}{
		{
			name: "GCM",
			opts: []csiptest.BootOption{
				csiptest.WithClientCert(clientCert),
				csiptest.WithClientCAsFile(rootsPath),
			},
		},
		{
			name: "CCM",
			opts: []csiptest.BootOption{
				csiptest.WithCCMMode(),
				csiptest.WithClientCert(clientCert),
				csiptest.WithClientCAsFile(rootsPath),
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := csiptest.BootServer(t, tc.opts...)
			httpClient := buildClient(t, srv.RootCA, clientCert)
			ctx := context.Background()

			// Step 2: POST EndDevice. The CSIP server overrides
			// client-supplied SFDI/LFDI with values derived from the
			// presented client cert (see internal/handler/edev.go), so
			// the POST body itself is intentionally minimal — the
			// identity binding under test is the cert path, not the
			// XML payload.
			location := postEndDevice(t, httpClient, srv.BaseURL)

			// Step 3: GET the returned EndDevice and assert identity.
			assertEndDeviceIdentityFromCert(t, ctx, srv.Client(), location, wantSFDI, wantLFDI)

			// Step 4: GET /sdev and assert non-empty identity. This is
			// the regression guard on IEEE-001: pre-fix, /sdev returned
			// empty <sFDI/><lFDI/> under both GCM (and silently under
			// CCM). The cert-derived values here are the booted server's
			// own ephemeral leaf cert, NOT the SunSpec client cert.
			assertSelfDeviceIdentityNonEmpty(t, ctx, srv.Client(), srv.ServerCert)
		})
	}
}

// postEndDevice issues POST /edev with a minimal body and returns the
// Location header. Asserts 201 Created (new) or 200 OK (already
// registered for this SFDI — second-call idempotency from edev.go).
// The Location header is RFC 7231 §7.1.2 compliant: an absolute path
// like "/edev/65DE1159" that callers compose with srv.BaseURL.
func postEndDevice(t *testing.T, client *http.Client, baseURL string) string {
	t.Helper()

	// Minimal EndDevice body. The server overrides SFDI/LFDI from the
	// client cert; ChangedTime gets stamped by the handler. We send
	// just enough XML to round-trip the unmarshal path so a malformed-
	// body 400 from the handler can be distinguished from a real
	// identity-binding failure later in the test.
	body := bytes.NewReader([]byte(`<EndDevice xmlns="urn:ieee:std:2030.5:ns"></EndDevice>`))
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+"/edev", body)
	if err != nil {
		t.Fatalf("build POST /edev: %v", err)
	}
	req.Header.Set("Content-Type", "application/sep+xml")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /edev: %v", err)
	}
	defer drain(resp.Body)

	switch resp.StatusCode {
	case http.StatusCreated, http.StatusOK:
		// 201 = fresh registration, 200 = idempotent re-register.
		// Both satisfy the procedure; the handler returns 200 only
		// when the cert's SFDI already has an EndDevice in the store.
	default:
		t.Fatalf("POST /edev: status = %d, want 201 or 200", resp.StatusCode)
	}

	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatalf("POST /edev: missing Location header on %d", resp.StatusCode)
	}
	return location
}

// assertEndDeviceIdentityFromCert GETs the EndDevice at the given path
// and asserts the response carries the expected cert-derived SFDI and
// LFDI. The href in the response is also sanity-checked against the
// Location header so a handler that returns the wrong record surfaces
// loudly rather than passing on an unrelated EndDevice's identity.
func assertEndDeviceIdentityFromCert(t *testing.T, ctx context.Context, c *csiptest.Client, location, wantSFDI, wantLFDI string) {
	t.Helper()

	var dev sep2.EndDevice
	if err := c.WalkLink(ctx, sep2.Link{Href: location}, &dev); err != nil {
		t.Fatalf("GET %s: %v", location, err)
	}

	if dev.Href != location {
		t.Errorf("EndDevice.Href = %q, want %q (server returned a different record than Location pointed at)", dev.Href, location)
	}
	if dev.SFDI != wantSFDI {
		t.Errorf("EndDevice.SFDI = %q, want %q (cert-derived)", dev.SFDI, wantSFDI)
	}
	if dev.LFDI != wantLFDI {
		t.Errorf("EndDevice.LFDI = %q, want %q (cert-derived)", dev.LFDI, wantLFDI)
	}
}

// assertSelfDeviceIdentityNonEmpty GETs /sdev and asserts the response
// carries non-empty SFDI and LFDI. This is the IEEE-001 regression
// guard: pre-fix, NewRouter closed over empty strings and the handler
// rendered <sFDI/><lFDI/> under both GCM and CCM. The expected values
// here are the BOOTED server's own ephemeral leaf cert (not the
// SunSpec client cert), so we derive them at assertion time and
// compare for equality. Equality is stronger than "non-empty" but
// catches the same regression PLUS a hypothetical future bug where
// /sdev renders some unrelated identity (e.g. the client's by
// accident).
func assertSelfDeviceIdentityNonEmpty(t *testing.T, ctx context.Context, c *csiptest.Client, serverCertPEM []byte) {
	t.Helper()

	block, _ := pem.Decode(serverCertPEM)
	if block == nil {
		t.Fatalf("decode server cert PEM: empty block")
	}
	serverLeaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}
	wantSFDI := sepTLS.SFDI(serverLeaf)
	wantLFDI := sepTLS.LFDI(serverLeaf)

	var sdev sep2.SelfDevice
	if err := c.WalkLink(ctx, sep2.Link{Href: "/sdev"}, &sdev); err != nil {
		t.Fatalf("GET /sdev: %v", err)
	}

	if sdev.SFDI == "" {
		t.Errorf("SelfDevice.SFDI is empty — IEEE-001 regression (NewRouter not fed cert-derived identity)")
	}
	if sdev.LFDI == "" {
		t.Errorf("SelfDevice.LFDI is empty — IEEE-001 regression (NewRouter not fed cert-derived identity)")
	}
	if sdev.SFDI != wantSFDI {
		t.Errorf("SelfDevice.SFDI = %q, want %q (server's own cert-derived value)", sdev.SFDI, wantSFDI)
	}
	if sdev.LFDI != wantLFDI {
		t.Errorf("SelfDevice.LFDI = %q, want %q (server's own cert-derived value)", sdev.LFDI, wantLFDI)
	}
}

// loadSunSpecCert reads the SunSpec V1.2 test PKI from disk and returns
// a parsed tls.Certificate. Fails the test if either PEM is malformed
// — by the time this runs, resolveFixtures has already confirmed both
// paths exist.
func loadSunSpecCert(t *testing.T, certPath, keyPath string) tls.Certificate {
	t.Helper()

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read SunSpec cert %s: %v", certPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read SunSpec key %s: %v", keyPath, err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse SunSpec cert+key: %v", err)
	}
	return cert
}
