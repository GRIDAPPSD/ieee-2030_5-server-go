// CSIP V1.2 section 8.1 - DER Identification. Tests EndDevice identity matches
// cert-derived SFDI/LFDI, and SelfDevice carries non-empty identity under
// both GCM and CCM (regression on #1 fix).
//
// V1.2 procedure step -> assertion mapping (per V1.2 section 8.1):
//
//	Step 1 (Boot server with a known device cert) ---------- csiptest.BootServer with the committed test device cert
//	Step 2 (Client POSTs an EndDevice to /edev) ------------ postEndDevice; assert 201 + Location header
//	Step 3 (GET the returned EndDevice; assert sFDI/lFDI) -- assertEndDeviceIdentityFromCert
//	Step 4 (GET /sdev; assert non-empty sFDI/lFDI) --------- assertSelfDeviceIdentityNonEmpty
//	         under BOTH cipher modes - regression on #1
//
// V1.2 section 3.2.3 specifies PIN = 111115 for the DER identification flow.
// The procedure here exercises identity binding via the client cert
// only; PIN handling lives in the Registration resource (separate
// ticket on the client side: #42 / #44). PIN value is
// documented for cross-reference, not asserted by BASIC-001.
//
// What this test relies on:
//   - csiptest.BootServer wiring serverSFDI/serverLFDI into NewRouter
//     from the booted server's leaf cert (#1 parity for the
//     in-process harness). Without that, /sdev returns empty identity
//     under both modes and Step 4 fails - which IS the regression this
//     test guards against.
//   - The committed, self-minted test device PKI under
//     testdata/csip-pki/testdevice/. Certificate provenance is
//     irrelevant to this procedure: the identity asserted is derived
//     from whatever leaf is presented, so this runs on every build
//     with no external material and no environment variable (403).
//
// The CCM-mode subtest still drives the server-side gotls listener;
// the stdlib http.Client offers GCM ciphers and the gotls server
// accepts either GCM or CCM-8. Tightening to "negotiated CCM-8 only"
// is gated on #21/#22 (Phase 8 follow-up via #62).
package csip_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"gopkg.in/yaml.v3"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// basic001IdentityFixture records the committed test device PKI's SFDI and
// LFDI. The expected identity is derived from the certificate at test time and
// compared against this file, so a regenerated PKI surfaces as a named fixture
// to update rather than as a stale constant in Go source.
const basic001IdentityFixture = "basic-001-testdevice-edev.yaml"

// TestBASIC_001_DERIdentification implements CSIP V1.2 8.1.
func TestBASIC_001_DERIdentification(t *testing.T) {
	t.Parallel()

	chainPath := filepath.Join(testdevicePKIRel, "device_chain.pem")
	keyPath := filepath.Join(testdevicePKIRel, "device_key.pem")
	rootsPath := filepath.Join(testdevicePKIRel, "root_ca.pem")

	clientCert := loadDeviceCert(t, chainPath, keyPath)

	leaf, err := x509.ParseCertificate(clientCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse test device leaf: %v", err)
	}

	// Asserted here because nothing downstream would notice its absence: the
	// server hook acknowledges a HardwareModuleName SAN without requiring one.
	if err := csipDeviceCertSAN(leaf); err != nil {
		t.Fatalf("test device leaf at %s: %v", chainPath, err)
	}

	// The expectation comes from the in-test 6.3.3 derivation and never from
	// sepTLS: a value produced by the helper under test agrees with it by
	// construction and would assert nothing.
	wantSFDI, wantLFDI := deriveDeviceIdentity(clientCert.Certificate[0])

	// Two implementations over the same bytes on disk, so a disagreement here
	// is a logic fault in one of them and cannot be a certificate roll. Which
	// one is not knowable from the values, so the message says so.
	if got := sepTLS.SFDI(leaf); got != wantSFDI {
		t.Fatalf("sepTLS.SFDI = %q but deriveDeviceIdentity = %q on the same DER: not a certificate roll, so one of those two is wrong; check both", got, wantSFDI)
	}
	if got := sepTLS.LFDI(leaf); got != wantLFDI {
		t.Fatalf("sepTLS.LFDI = %q but deriveDeviceIdentity = %q on the same DER: not a certificate roll, so one of those two is wrong; check both", got, wantLFDI)
	}

	// The fixture is the recorded identity rather than a second computation of
	// it, so a disagreement here is a roll and cannot be a helper regression.
	pinned := readIdentityPin(t, filepath.Join("fixtures", basic001IdentityFixture))
	if pinned.SFDI != wantSFDI {
		t.Fatalf("fixture %s pins SFDI %q but %s derives %q: the PKI was regenerated; update the fixture and testdata/csip-pki/testdevice/README.md", basic001IdentityFixture, pinned.SFDI, chainPath, wantSFDI)
	}
	if pinned.LFDI != wantLFDI {
		t.Fatalf("fixture %s pins LFDI %q but %s derives %q: the PKI was regenerated; update the fixture and testdata/csip-pki/testdevice/README.md", basic001IdentityFixture, pinned.LFDI, chainPath, wantLFDI)
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
			// the POST body itself is intentionally minimal - the
			// identity binding under test is the cert path, not the
			// XML payload.
			location := postEndDevice(t, httpClient, srv.BaseURL)

			// Step 3: GET the returned EndDevice and assert identity.
			assertEndDeviceIdentityFromCert(t, ctx, srv.Client(), location, wantSFDI, wantLFDI)

			// Step 4: GET /sdev and assert non-empty identity. This is
			// the regression guard on #1: pre-fix, /sdev returned
			// empty <sFDI/><lFDI/> under both GCM (and silently under
			// CCM). The cert-derived values here are the booted server's
			// own ephemeral leaf cert, NOT the SunSpec client cert.
			assertSelfDeviceIdentityNonEmpty(t, ctx, srv.Client(), srv.ServerCert)
		})
	}
}

// postEndDevice issues POST /edev with a minimal body and returns the
// Location header. Asserts 201 Created (new) or 200 OK (already
// registered for this SFDI - second-call idempotency from edev.go).
// The Location header is RFC 7231 section 7.1.2 compliant: an absolute path
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
// carries non-empty SFDI and LFDI. This is the #1 regression
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
		t.Errorf("SelfDevice.SFDI is empty - #1 regression (NewRouter not fed cert-derived identity)")
	}
	if sdev.LFDI == "" {
		t.Errorf("SelfDevice.LFDI is empty - #1 regression (NewRouter not fed cert-derived identity)")
	}
	if sdev.SFDI != wantSFDI {
		t.Errorf("SelfDevice.SFDI = %q, want %q (server's own cert-derived value)", sdev.SFDI, wantSFDI)
	}
	if sdev.LFDI != wantLFDI {
		t.Errorf("SelfDevice.LFDI = %q, want %q (server's own cert-derived value)", sdev.LFDI, wantLFDI)
	}
}

// loadDeviceCert reads a device chain and key from disk and returns the parsed
// pair. A missing file is a failure rather than a skip: this material is
// committed, so its absence means a damaged checkout.
func loadDeviceCert(t *testing.T, chainPath, keyPath string) tls.Certificate {
	t.Helper()

	chainPEM, err := os.ReadFile(chainPath)
	if err != nil {
		t.Fatalf("read device chain %s: %v", chainPath, err)
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read device key %s: %v", keyPath, err)
	}
	cert, err := tls.X509KeyPair(chainPEM, keyPEM)
	if err != nil {
		t.Fatalf("parse device chain+key: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Fatalf("device chain %s parsed to an empty certificate list", chainPath)
	}
	return cert
}

// deriveDeviceIdentity computes the IEEE 2030.5 6.3.3 identity from a leaf's
// DER: the LFDI is the first 20 bytes of SHA-256(DER) hex-uppercase, and the
// SFDI is the top 36 bits of that hash as 11 zero-padded decimal digits plus a
// sum-of-digits mod-10 check digit.
//
// Spelled out here rather than called from sepTLS so the comparison above has a
// separate copy on each side: that catches a later edit to either one, but not a
// misreading of the spec shared by both from the start.
func deriveDeviceIdentity(der []byte) (sfdi, lfdi string) {
	sum := sha256.Sum256(der)
	lfdi = strings.ToUpper(hex.EncodeToString(sum[:20]))

	top36 := uint64(sum[0])<<28 | uint64(sum[1])<<20 | uint64(sum[2])<<12 | uint64(sum[3])<<4 | uint64(sum[4])>>4
	digits := fmt.Sprintf("%011d", top36)
	total := 0
	for _, d := range digits {
		total += int(d - '0')
	}
	return fmt.Sprintf("%s%d", digits, (10-total%10)%10), lfdi
}

// readIdentityPin returns the single EndDevice recorded in a harness fixture.
// Strict-field decoding through csiptest.Spec means a mistyped key fails loudly
// instead of reading as an empty identity that no assertion could catch.
func readIdentityPin(t *testing.T, path string) csiptest.EndDeviceSpec {
	t.Helper()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read identity fixture %s: %v", path, err)
	}
	var spec csiptest.Spec
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&spec); err != nil {
		t.Fatalf("decode identity fixture %s: %v", path, err)
	}
	if len(spec.EndDevices) != 1 {
		t.Fatalf("identity fixture %s carries %d EndDevices, want exactly one", path, len(spec.EndDevices))
	}
	return spec.EndDevices[0]
}
