// CSIP V1.2 section 5.4 - HTTP Request Semantics.
//
// CORE-001 walks the DeviceCapability subtree the server advertises and
// asserts that every advertised resource behaves correctly under the
// matrix of HTTP methods defined by the IEEE-2030.5 ACL
// (internal/auth/acl.go DefaultACLRules) and the per-handler method
// whitelists in internal/handler/. Specifically:
//
//   - Permitted methods on advertised resources return a 2xx status.
//   - Disallowed methods on advertised resources return 405 Method Not
//     Allowed with an Allow response header (RFC 7231 section 6.5.5).
//   - Malformed bodies on POST endpoints return 400 Bad Request.
//
// This test is the workhorse smoke for the CSIP server: in one boot it
// drives 8+ handlers under internal/handler/ (dcap, time, sdev, edev list,
// mup list, dc list, upt list, msg list, rsps list) and proves that
// router wiring, method gating, ACL middleware, and XML serialization all
// agree end-to-end.
//
// V1.2 procedure step -> assertion mapping (per V1.2 section 5.4 procedure):
//
//	Step 1 (GET /dcap and parse DeviceCapability) -> Step1 GET /dcap
//	Step 2 (each advertised link returns 200 on GET) -> loop over advertisedLinks(dcap)
//	Step 3 (disallowed methods produce 405 + Allow header) -> assertMethodNotAllowed
//	Step 4 (malformed POST body produces 400) -> assertMalformedBodyRejected
//
// All requests are issued through a single mTLS client wired to the
// same CA + device cert that csiptest.BootServer is configured with via
// WithClientCAsFile + WithClientCert, so the ACL chain (IdentityMiddleware
// -> ACLMiddleware -> mux) is in play for every assertion.
package csip_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/test/csip/csiptest"
)

// Test files in this package share these helpers:
//   - mustBuildClientPKI       - generates a CA + device cert under our
//                                control. Used by both CORE-001 and CORE-002
//                                so BootServer can be told to trust our CA.
//   - buildClient              - wires an *http.Client to a booted server
//                                using the server's RootCA + our device cert.
//   - drain / mustNewRequest   - request-plumbing helpers used by the
//                                CORE-001 assertion helpers; CORE-002 builds
//                                requests inline.

// TestCORE_001_HTTPRequest implements CSIP V1.2 section 5.4.
func TestCORE_001_HTTPRequest(t *testing.T) {
	t.Parallel()

	// Build a CA + device cert under our control so the test can run
	// an *http.Client with the same identity as csiptest.BootServer's
	// internal client. csiptest exposes RootCA but not the CA key (we
	// could not sign a new device cert against the booted server's
	// default ephemeral CA), and it does not expose the wired
	// *http.Client either - so we bring our own and tell BootServer
	// to trust it via WithClientCAsFile + WithClientCert.
	_, caCertFile, clientCert := mustBuildClientPKI(t)

	srv := csiptest.BootServer(t,
		csiptest.WithClientCAsFile(caCertFile),
		csiptest.WithClientCert(clientCert),
	)

	// Build the *http.Client we use for every assertion below. It
	// trusts the booted server's ephemeral server-leaf via RootCA and
	// presents the device cert we just generated.
	httpClient := buildClient(t, srv.RootCA, clientCert)

	ctx := context.Background()

	// Step 1: GET /dcap returns 200 and parses as a DeviceCapability.
	// We reuse csiptest.Client.GetDeviceCapability here because the
	// happy-path chained-GET helper exists for exactly this case.
	dcap, err := srv.Client().GetDeviceCapability(ctx)
	if err != nil {
		t.Fatalf("step 1: GET /dcap: %v", err)
	}
	if dcap.Href != "/dcap" {
		t.Fatalf("step 1: DeviceCapability.Href = %q, want %q", dcap.Href, "/dcap")
	}

	// Step 2: every advertised link returns 200 on GET.
	links := advertisedLinks(dcap)
	if len(links) < 7 {
		// CORE-001 specifically claims "exercises 7+ handlers in one
		// test". If the DCAP shrinks below that, surface it loudly
		// so the test stays the conformance smoke the matrix advertises.
		t.Fatalf("step 2: DeviceCapability advertised only %d links; want >= 7", len(links))
	}
	t.Logf("step 2: walking %d advertised links from /dcap", len(links))
	for _, link := range links {
		link := link // capture for subtest
		t.Run("GET_"+link.label, func(t *testing.T) {
			t.Parallel()
			assertGetOK(t, httpClient, srv.BaseURL, link.href)
		})
	}

	// Step 3: disallowed methods on advertised resources return 405.
	// Representatives chosen across the three ACL classes:
	//   - read-only resources (/dcap, /tm, /sdev, /dc) reject POST/PUT/DELETE.
	//   - postable lists (/mup, /upt) reject PUT/DELETE.
	//   - /edev accepts GET/POST/PUT/HEAD but the collection rejects DELETE.
	disallowed := []struct {
		path    string
		method  string
		comment string
	}{
		{"/dcap", http.MethodPost, "DeviceCapability is GET/HEAD only"},
		{"/dcap", http.MethodDelete, "DeviceCapability is GET/HEAD only"},
		{"/tm", http.MethodPost, "Time is GET/HEAD only"},
		{"/sdev", http.MethodPut, "SelfDevice is GET/HEAD only"},
		{"/dc", http.MethodPost, "DERCurveList is read-only at the collection level"},
		{"/mup", http.MethodDelete, "MirrorUsagePoint collection forbids DELETE"},
		{"/upt", http.MethodDelete, "UsagePoint collection forbids DELETE"},
	}
	for _, d := range disallowed {
		d := d
		t.Run("405_"+d.method+"_"+d.path, func(t *testing.T) {
			t.Parallel()
			assertMethodNotAllowed(t, httpClient, srv.BaseURL, d.method, d.path)
		})
	}

	// Step 4: malformed body on a POST endpoint returns 400 Bad Request.
	// /edev accepts POST (registers an EndDevice). Feeding it junk XML
	// must drop a 400 - the handler returns http.StatusBadRequest on
	// body-read and xml.Unmarshal errors.
	t.Run("400_malformed_POST_/edev", func(t *testing.T) {
		t.Parallel()
		assertMalformedBodyRejected(t, httpClient, srv.BaseURL, "/edev")
	})
}

// linkRef is a (href, label) pair extracted from the parsed
// DeviceCapability so subtest names read like "GET_TimeLink" instead of
// "GET_/tm".
type linkRef struct {
	href  string
	label string
}

// advertisedLinks hand-walks DeviceCapability and returns every populated
// link / list-link. Reflection would be terser, but a hand walk is what
// any reviewer reading the V1.2 section 5.4 procedure expects to see, and the
// field set is fixed at 13 by the XSD (10 FSA-base + 3 extension links).
func advertisedLinks(dcap sep2.DeviceCapability) []linkRef {
	var out []linkRef
	add := func(href, label string) {
		if href == "" {
			return
		}
		out = append(out, linkRef{href: href, label: label})
	}
	// FunctionSetAssignmentsBase elements.
	if l := dcap.CustomerAccountListLink; l != nil {
		add(l.Href, "CustomerAccountListLink")
	}
	if l := dcap.DemandResponseProgramListLink; l != nil {
		add(l.Href, "DemandResponseProgramListLink")
	}
	if l := dcap.DERProgramListLink; l != nil {
		add(l.Href, "DERProgramListLink")
	}
	if l := dcap.FileListLink; l != nil {
		add(l.Href, "FileListLink")
	}
	if l := dcap.MessagingProgramListLink; l != nil {
		add(l.Href, "MessagingProgramListLink")
	}
	if l := dcap.PrepaymentListLink; l != nil {
		add(l.Href, "PrepaymentListLink")
	}
	if l := dcap.ResponseSetListLink; l != nil {
		add(l.Href, "ResponseSetListLink")
	}
	if l := dcap.TariffProfileListLink; l != nil {
		add(l.Href, "TariffProfileListLink")
	}
	if l := dcap.TimeLink; l != nil {
		add(l.Href, "TimeLink")
	}
	if l := dcap.UsagePointListLink; l != nil {
		add(l.Href, "UsagePointListLink")
	}
	// DeviceCapability extensions.
	if l := dcap.EndDeviceListLink; l != nil {
		add(l.Href, "EndDeviceListLink")
	}
	if l := dcap.MirrorUsagePointListLink; l != nil {
		add(l.Href, "MirrorUsagePointListLink")
	}
	if l := dcap.SelfDeviceLink; l != nil {
		add(l.Href, "SelfDeviceLink")
	}
	return out
}

// assertGetOK issues a GET against path and asserts a 2xx status. List
// resources return 200; we accept any 2xx so the helper does not need to
// know which advertised links are lists.
func assertGetOK(t *testing.T, client *http.Client, baseURL, path string) {
	t.Helper()
	req := mustNewRequest(t, http.MethodGet, baseURL+path, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer drain(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("GET %s: status = %d, want 2xx", path, resp.StatusCode)
	}
}

// assertMethodNotAllowed asserts that issuing method against path
// produces 405 with an Allow header. We require the Allow header per
// RFC 7231 section 6.5.5 and per the ACL middleware behaviour (acl.go sets
// Allow before writing 405).
func assertMethodNotAllowed(t *testing.T, client *http.Client, baseURL, method, path string) {
	t.Helper()
	req := mustNewRequest(t, method, baseURL+path, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer drain(resp.Body)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("%s %s: status = %d, want %d", method, path, resp.StatusCode, http.StatusMethodNotAllowed)
	}
	if got := resp.Header.Get("Allow"); got == "" {
		t.Errorf("%s %s: missing Allow header on 405 (RFC 7231 section 6.5.5)", method, path)
	}
}

// assertMalformedBodyRejected POSTs garbage to path and asserts the
// server rejects it with 400 Bad Request. The IEEE-2030.5 server
// distinguishes XML-parse failure (400) from semantic-validation failure
// (also 400 in practice) - both satisfy section 5.4 step 4 ("malformed request").
func assertMalformedBodyRejected(t *testing.T, client *http.Client, baseURL, path string) {
	t.Helper()
	body := bytes.NewReader([]byte("this is not XML at all"))
	req := mustNewRequest(t, http.MethodPost, baseURL+path, body)
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer drain(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST %s with junk body: status = %d, want %d", path, resp.StatusCode, http.StatusBadRequest)
	}
}

// mustNewRequest builds an *http.Request bound to a fresh background
// context. The harness sets per-test timeouts on the http.Client itself.
func mustNewRequest(t *testing.T, method, url string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	return req
}

// drain consumes and closes the body. csiptest.Client does this
// internally; helpers in this file talk to http.Response directly and
// must do it themselves to avoid keep-alive leaks across subtests.
func drain(rc io.ReadCloser) {
	if rc == nil {
		return
	}
	_, _ = io.Copy(io.Discard, rc)
	_ = rc.Close()
}

// mustBuildClientPKI generates an ephemeral CA + device-leaf pair and
// returns (caCertPEM, caCertFilePath, deviceCert). The CA cert is also
// written to disk so it can be handed to csiptest.WithClientCAsFile.
//
// Why this exists: csiptest.BootServer generates its own ephemeral CA
// for the default client cert path, but it does not expose the CA key,
// so a downstream test can't sign new certs against the same root. We
// take ownership of the trust root via WithClientCAsFile + WithClientCert
// and reuse it for the *http.Client this test drives directly.
func mustBuildClientPKI(t *testing.T) (caCertPEM []byte, caCertFile string, deviceCert tls.Certificate) {
	t.Helper()

	caCertPEM, caKeyPEM, err := certs.GenerateCA(certs.CAOptions{
		CommonName: "CORE-001 Test CA",
		ValidYears: 1,
	})
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	caCert, err := certs.ParseCertificatePEM(caCertPEM)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(caKeyPEM)
	if err != nil {
		t.Fatalf("parse CA key: %v", err)
	}
	devCertPEM, devKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "CORE-001-DEVICE",
	})
	if err != nil {
		t.Fatalf("generate device cert: %v", err)
	}
	deviceCert, err = tls.X509KeyPair(devCertPEM, devKeyPEM)
	if err != nil {
		t.Fatalf("parse device cert: %v", err)
	}

	dir := t.TempDir()
	caCertFile = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caCertFile, caCertPEM, 0o600); err != nil {
		t.Fatalf("write CA pem: %v", err)
	}
	return caCertPEM, caCertFile, deviceCert
}

// buildClient constructs an *http.Client wired to the booted server's
// server-leaf CA and presenting deviceCert on every request. Timeout
// matches csiptest's default (10s) so a hung handler under test fails
// fast rather than wedging the test binary.
func buildClient(t *testing.T, serverRootCA []byte, deviceCert tls.Certificate) *http.Client {
	t.Helper()
	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(serverRootCA) {
		t.Fatalf("append server root CA")
	}
	return &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{deviceCert},
				RootCAs:      rootPool,
				ServerName:   "127.0.0.1",
				MinVersion:   tls.VersionTLS12,
				MaxVersion:   tls.VersionTLS12,
			},
		},
	}
}
