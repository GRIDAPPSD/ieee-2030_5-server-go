package assembly_test

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// URL-index addressing.
//
// Resource URLs address an EndDevice by an opaque, server-chosen index
// ("/edev/3/rg") rather than by the certificate-derived LFDI. These tests
// assert the SERVED BYTES, per [[data-invariants]] Rule 1: it is not enough
// that a request succeeds, the href actually written on the wire has to be
// the index form and has to resolve.
//
// The most important test in this file is
// TestURLIndex_StaleIndexPointingAtAnotherDeviceIs403. Indices are stable
// when persistence is configured, but a client can still hold a URL captured
// before a fleet change or a migration. If such a URL now addresses a
// DIFFERENT device, the request must be denied on the caller's certificate,
// not served. Silent cross-device access would be far worse than either URL
// scheme, so that gate is the safety net for the whole change.

// deviceLFDIA and deviceLFDIB are two canonical 40-hex-character LFDIs
// (IEEE 2030.5 section 6.3.4 form). Their length is load-bearing: the
// negative guard below searches served hrefs for exactly this shape.
const (
	deviceLFDIA = "0BA1C3D4E5F60718293A4B5C6D7E8F9012345678"
	deviceSFDIA = "1122334455667788"
	deviceLFDIB = "F0E1D2C3B4A5968778695A4B3C2D1E0F87654321"
	deviceSFDIB = "8877665544332211"
)

// lfdiCtxKey types the context slot the fake mTLS layer writes the caller's
// certificate-derived identity into.
type lfdiCtxKey struct{}

type callerIdentity struct{ lfdi, sfdi string }

// indexTestPolicy builds an AuthPolicy whose Identity comes from a per-request
// stand-in for the client certificate, NOT from the URL. The test client sets
// the X-Test-LFDI header; Wrap moves it into the request context exactly where
// real identity middleware would put the value derived from
// r.TLS.PeerCertificates[0]. Keeping identity request-scoped is what lets a
// single server answer as two different devices, which is the only way to test
// the ownership gate honestly.
func indexTestPolicy() assembly.AuthPolicy {
	known := map[string]callerIdentity{
		deviceLFDIA: {deviceLFDIA, deviceSFDIA},
		deviceLFDIB: {deviceLFDIB, deviceSFDIB},
	}
	return assembly.AuthPolicy{
		Wrap: func(h http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if id, ok := known[r.Header.Get("X-Test-LFDI")]; ok {
					r = r.WithContext(context.WithValue(r.Context(), lfdiCtxKey{}, id))
				}
				h.ServeHTTP(w, r)
			})
		},
		Identity: func(ctx context.Context) (string, string, bool) {
			id, ok := ctx.Value(lfdiCtxKey{}).(callerIdentity)
			if !ok {
				return "", "", false
			}
			return id.lfdi, id.sfdi, true
		},
		SFDIPrefix: func(sfdi string) (string, error) {
			if len(sfdi) < 8 {
				return "", errShortSFDI
			}
			return sfdi[:8], nil
		},
	}
}

var errShortSFDI = errShort("SFDI too short")

type errShort string

func (e errShort) Error() string { return string(e) }

// indexTestServer wires a router whose EndDevice index is persistence-backed
// at path, so a test can restart it against the same assignments.
func indexTestServer(t *testing.T, path string) (*httptest.Server, *assembly.Stores) {
	t.Helper()

	idx, err := memory.NewEndDeviceIndexWithPersistence(path)
	if err != nil {
		t.Fatalf("open EndDevice index at %q: %v", path, err)
	}
	stores := testStores()
	stores.EndDeviceIndexes = idx

	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, indexTestPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, stores
}

// do issues a request to the server as the device identified by asLFDI (the
// stand-in for presenting that device's certificate) and returns the status
// and body.
func do(t *testing.T, srv *httptest.Server, method, path, asLFDI string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if asLFDI != "" {
		req.Header.Set("X-Test-LFDI", asLFDI)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s %s: %v", method, path, err)
	}
	return resp.StatusCode, string(body)
}

// register performs the POST /edev self-registration for one device and
// returns the served EndDevice.
func register(t *testing.T, srv *httptest.Server, asLFDI string) sep2.EndDevice {
	t.Helper()
	status, body := do(t, srv, http.MethodPost, "/edev", asLFDI)
	if status != http.StatusCreated {
		t.Fatalf("POST /edev as %s: status %d, want 201; body=%s", asLFDI, status, body)
	}
	var dev sep2.EndDevice
	if err := xml.Unmarshal([]byte(body), &dev); err != nil {
		t.Fatalf("decode created EndDevice: %v; body=%s", err, body)
	}
	return dev
}

// TestURLIndex_CreatedEndDeviceUsesIndexHrefs asserts the exact href strings
// stamped on a freshly registered device. These are the links a client
// follows, so they are asserted as exact strings, not as "contains /edev".
func TestURLIndex_CreatedEndDeviceUsesIndexHrefs(t *testing.T) {
	t.Parallel()

	srv, _ := indexTestServer(t, filepath.Join(t.TempDir(), "edevindex.json"))
	dev := register(t, srv, deviceLFDIA)

	if dev.Href != "/edev/1" {
		t.Errorf("EndDevice.Href = %q, want %q", dev.Href, "/edev/1")
	}
	if dev.RegistrationLink == nil {
		t.Fatal("RegistrationLink is nil")
	}
	if dev.RegistrationLink.Href != "/edev/1/rg" {
		t.Errorf("RegistrationLink.Href = %q, want %q", dev.RegistrationLink.Href, "/edev/1/rg")
	}
	if dev.FunctionSetAssignmentsListLink == nil {
		t.Fatal("FunctionSetAssignmentsListLink is nil")
	}
	if dev.FunctionSetAssignmentsListLink.Href != "/edev/1/fsa" {
		t.Errorf("FunctionSetAssignmentsListLink.Href = %q, want %q",
			dev.FunctionSetAssignmentsListLink.Href, "/edev/1/fsa")
	}

	// Identity is unchanged: only addressing moved to the index.
	if dev.LFDI != deviceLFDIA {
		t.Errorf("EndDevice.LFDI = %q, want %q: identity must stay the certificate-derived LFDI",
			dev.LFDI, deviceLFDIA)
	}
	if dev.SFDI != deviceSFDIA {
		t.Errorf("EndDevice.SFDI = %q, want %q", dev.SFDI, deviceSFDIA)
	}
}

// TestURLIndex_EndDeviceListEntryHrefsAreIndexForm asserts the hrefs a client
// actually discovers by walking GET /edev, for a two-device fleet.
func TestURLIndex_EndDeviceListEntryHrefsAreIndexForm(t *testing.T) {
	t.Parallel()

	srv, _ := indexTestServer(t, filepath.Join(t.TempDir(), "edevindex.json"))
	register(t, srv, deviceLFDIA)
	register(t, srv, deviceLFDIB)

	status, body := do(t, srv, http.MethodGet, "/edev", deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET /edev: status %d, want 200; body=%s", status, body)
	}
	var list sep2.EndDeviceList
	if err := xml.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("decode EndDeviceList: %v; body=%s", err, body)
	}
	if len(list.EndDevice) != 2 {
		t.Fatalf("EndDeviceList carries %d devices, want 2; body=%s", len(list.EndDevice), body)
	}

	// The list is key-ordered, and keys are the indices "1" and "2".
	byLFDI := map[string]sep2.EndDevice{}
	for _, d := range list.EndDevice {
		byLFDI[d.LFDI] = d
	}
	for _, tc := range []struct{ lfdi, wantHref string }{
		{deviceLFDIA, "/edev/1"},
		{deviceLFDIB, "/edev/2"},
	} {
		got, ok := byLFDI[tc.lfdi]
		if !ok {
			t.Errorf("device %s absent from EndDeviceList", tc.lfdi)
			continue
		}
		if got.Href != tc.wantHref {
			t.Errorf("device %s href = %q, want %q", tc.lfdi, got.Href, tc.wantHref)
		}
	}
}

// TestURLIndex_EveryEndDeviceLinkResolves asserts each link stamped on a
// served EndDevice is an index-form href AND that a GET on it reaches a real
// route. A link set that is converted but does not resolve is exactly the
// half-converted failure this change must not ship: a client following links
// would land on a 404.
func TestURLIndex_EveryEndDeviceLinkResolves(t *testing.T) {
	t.Parallel()

	srv, stores := indexTestServer(t, filepath.Join(t.TempDir(), "edevindex.json"))
	dev := register(t, srv, deviceLFDIA)

	ctx := context.Background()
	// Seed the resources those links point at, keyed by the INDEX, which is
	// what the path now carries. The Registration is deliberately NOT among
	// them: registering the device created it, and seeding one here would
	// make this test pass whether or not that coupling holds.
	derCap := sep2.DERCapability{}
	derCap.Href = "/edev/1/der/1/dercap"
	if err := stores.DERCapabilities.Create(ctx, "1/1", coresingleton.SingletonKey, derCap); err != nil {
		t.Fatalf("seed DERCapability: %v", err)
	}

	links := []struct {
		name string
		href string
	}{
		{"Href", dev.Href},
		{"RegistrationLink", dev.RegistrationLink.Href},
		{"FunctionSetAssignmentsListLink", dev.FunctionSetAssignmentsListLink.Href},
	}
	for _, l := range links {
		if !strings.HasPrefix(l.href, "/edev/1") {
			t.Errorf("%s = %q, want an /edev/1 index-form href", l.name, l.href)
		}
		status, body := do(t, srv, http.MethodGet, l.href, deviceLFDIA)
		if status != http.StatusOK {
			t.Errorf("GET %s (%s): status %d, want 200; body=%s", l.href, l.name, status, body)
		}
	}

	// The DER family hrefs are built by the handlers from the path segment,
	// so they follow the index automatically. Assert the served bytes rather
	// than trusting that.
	status, body := do(t, srv, http.MethodGet, "/edev/1/der/1/dercap", deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET /edev/1/der/1/dercap: status %d, want 200; body=%s", status, body)
	}
	var served sep2.DERCapability
	if err := xml.Unmarshal([]byte(body), &served); err != nil {
		t.Fatalf("decode DERCapability: %v; body=%s", err, body)
	}
	if served.Href != "/edev/1/der/1/dercap" {
		t.Errorf("DERCapability.Href = %q, want %q", served.Href, "/edev/1/der/1/dercap")
	}

	// Registration's own href is stamped by its handler from the path.
	status, body = do(t, srv, http.MethodGet, "/edev/1/rg", deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("GET /edev/1/rg: status %d, want 200; body=%s", status, body)
	}
	var servedReg sep2.Registration
	if err := xml.Unmarshal([]byte(body), &servedReg); err != nil {
		t.Fatalf("decode Registration: %v; body=%s", err, body)
	}
	if servedReg.Href != "/edev/1/rg" {
		t.Errorf("Registration.Href = %q, want %q", servedReg.Href, "/edev/1/rg")
	}
}

// lfdiInHref matches an href attribute whose value contains a run of 40 or
// more hex characters: the shape of an IEEE 2030.5 LFDI. 40 is the exact
// canonical length (section 6.3.4); the open upper bound catches a longer
// identifier being spliced in as well.
var lfdiInHref = regexp.MustCompile(`href="[^"]*[0-9A-Fa-f]{40,}[^"]*"`)

// TestURLIndex_NoLFDIAppearsInAnyServedHref is the regression guard against a
// half-converted link set. It walks a representative served tree and asserts
// that no href anywhere carries an LFDI-shaped value.
//
// It deliberately inspects the RAW BYTES rather than decoded structs: a
// decoded struct only exposes the fields the test thought to check, whereas
// the bytes carry every href the server actually emitted, including ones a
// future change might add.
//
// This is a negative assertion, so it can pass vacuously if the tree is
// empty. The fleet is registered and the sub-resources are seeded first, and
// the test asserts a positive control (that the index form IS present) so a
// silently empty walk cannot be mistaken for a pass.
func TestURLIndex_NoLFDIAppearsInAnyServedHref(t *testing.T) {
	t.Parallel()

	srv, stores := indexTestServer(t, filepath.Join(t.TempDir(), "edevindex.json"))
	register(t, srv, deviceLFDIA)
	register(t, srv, deviceLFDIB)

	ctx := context.Background()
	// No Registration seed: registering the devices above created theirs.
	derCap := sep2.DERCapability{}
	derCap.Href = "/edev/1/der/1/dercap"
	if err := stores.DERCapabilities.Create(ctx, "1/1", coresingleton.SingletonKey, derCap); err != nil {
		t.Fatalf("seed DERCapability: %v", err)
	}
	der := sep2.DER{}
	der.Href = "/edev/1/der/1"
	der.DERCapabilityLink = &sep2.Link{Href: "/edev/1/der/1/dercap"}
	if err := stores.DERs.Create(ctx, "1", "1", der); err != nil {
		t.Fatalf("seed DER: %v", err)
	}

	paths := []string{
		"/dcap",
		"/tm",
		"/sdev",
		"/sdev/sdi",
		"/edev",
		"/edev/1",
		"/edev/2",
		"/edev/1/rg",
		"/edev/1/fsa",
		"/edev/1/der",
		"/edev/1/der/1/dercap",
		"/edev/1/der/1/derg",
		"/edev/1/der/1/ders",
		"/edev/1/der/1/dera",
		"/edev/1/cfg",
		"/edev/1/dstat",
		"/edev/1/ps",
		"/edev/1/log",
		"/edev/1/frq",
		"/edev/1/frp",
		"/edev/1/sub",
		"/mup",
		"/upt",
		"/rt",
		"/dc",
		"/msg",
		"/rsps",
	}

	sawIndexHref := false
	walked := 0
	for _, p := range paths {
		status, body := do(t, srv, http.MethodGet, p, deviceLFDIA)
		if status != http.StatusOK {
			// Not every optional resource is seeded; a 404 or 403 carries no
			// hrefs to inspect and is not what this test is about.
			continue
		}
		walked++
		if m := lfdiInHref.FindString(body); m != "" {
			t.Errorf("GET %s served an LFDI-shaped value inside an href: %s\nfull body: %s", p, m, body)
		}
		// Belt and braces: the literal test LFDIs must not appear in an href
		// even if some future LFDI form escapes the length regexp.
		for _, lfdi := range []string{deviceLFDIA, deviceLFDIB} {
			if strings.Contains(body, `href="/edev/`+lfdi) {
				t.Errorf("GET %s served an LFDI-addressed href for %s\nbody: %s", p, lfdi, body)
			}
		}
		if strings.Contains(body, `href="/edev/1`) || strings.Contains(body, `href="/edev/2`) {
			sawIndexHref = true
		}
	}

	if walked < 5 {
		t.Fatalf("walked only %d served documents; the negative guard would pass vacuously", walked)
	}
	if !sawIndexHref {
		t.Fatal("no index-form /edev href was seen anywhere in the walked tree; the negative guard would pass vacuously")
	}
}

// TestURLIndex_OwnershipGateDeniesCrossDeviceAccess asserts device B cannot
// read device A's Registration. The requesting device is identified by its
// certificate-derived LFDI, never by the index in the path, so supplying
// another device's index does not grant anything.
func TestURLIndex_OwnershipGateDeniesCrossDeviceAccess(t *testing.T) {
	t.Parallel()

	srv, _ := indexTestServer(t, filepath.Join(t.TempDir(), "edevindex.json"))
	devA := register(t, srv, deviceLFDIA)
	register(t, srv, deviceLFDIB)

	// No Registration seed: device A's was created with device A.
	// Control: the owner gets it.
	status, body := do(t, srv, http.MethodGet, devA.RegistrationLink.Href, deviceLFDIA)
	if status != http.StatusOK {
		t.Fatalf("owner GET %s: status %d, want 200; body=%s", devA.RegistrationLink.Href, status, body)
	}
	if !strings.Contains(body, itoa(testFixturePIN)) {
		t.Fatalf("owner GET did not return the provisioned PIN; body=%s", body)
	}

	// Device B, presenting its own certificate, asks for device A's index.
	status, body = do(t, srv, http.MethodGet, devA.RegistrationLink.Href, deviceLFDIB)
	if status != http.StatusForbidden {
		t.Errorf("cross-device GET %s as %s: status %d, want 403; body=%s",
			devA.RegistrationLink.Href, deviceLFDIB, status, body)
	}
	assertNoRegistrationLeak(t, body, testFixturePIN)

	// No certificate at all is also denied, and also leaks nothing.
	status, body = do(t, srv, http.MethodGet, devA.RegistrationLink.Href, "")
	if status != http.StatusForbidden {
		t.Errorf("unauthenticated GET %s: status %d, want 403; body=%s",
			devA.RegistrationLink.Href, status, body)
	}
	assertNoRegistrationLeak(t, body, testFixturePIN)
}

// TestURLIndex_StaleIndexPointingAtAnotherDeviceIs403 is the safety net for
// the entire index scheme.
//
// Indices are stable when persistence is configured, but a client can hold a
// URL captured before a fleet change, a migration, or a boot without a
// persistence path. If that stale index now addresses a DIFFERENT device, the
// request must be denied. A silent cross-device read would be strictly worse
// than a broken link, and worse than either URL scheme.
//
// The scenario is built deliberately rather than waited for: two servers
// backed by independent index state provision the same two devices in
// OPPOSITE order, so index "1" belongs to device A on the first and to device
// B on the second. Device A then presents its own certificate against the URL
// it legitimately learned from the first server.
func TestURLIndex_StaleIndexPointingAtAnotherDeviceIs403(t *testing.T) {
	t.Parallel()

	// First boot: device A registers first, so A holds "/edev/1".
	firstBoot, _ := indexTestServer(t, filepath.Join(t.TempDir(), "first.json"))
	devA := register(t, firstBoot, deviceLFDIA)
	register(t, firstBoot, deviceLFDIB)
	staleHref := devA.RegistrationLink.Href
	if staleHref != "/edev/1/rg" {
		t.Fatalf("precondition: device A learned %q, want %q", staleHref, "/edev/1/rg")
	}

	// Second boot with independent index state and the opposite provisioning
	// order: now "/edev/1" is device B.
	secondBoot, _ := indexTestServer(t, filepath.Join(t.TempDir(), "second.json"))
	devB := register(t, secondBoot, deviceLFDIB)
	register(t, secondBoot, deviceLFDIA)
	if devB.Href != "/edev/1" {
		t.Fatalf("precondition: device B did not take index 1, got %q", devB.Href)
	}

	// Device B's Registration under index 1 exists because device B was
	// registered, not because this test put it there. That is what makes the
	// leak assertion below meaningful: there is a real record behind the URL
	// device A is being denied.

	// Device A follows its stale URL, presenting device A's certificate. The
	// index now resolves to device B's record, whose stored LFDI is B's, so
	// the gate must deny.
	status, body := do(t, secondBoot, http.MethodGet, staleHref, deviceLFDIA)
	if status != http.StatusForbidden {
		t.Errorf("stale index %s followed by %s: status %d, want 403; body=%s",
			staleHref, deviceLFDIA, status, body)
	}
	assertNoRegistrationLeak(t, body, testFixturePIN)

	// And the device that DOES own that index is still served, so the gate
	// denies on identity rather than by breaking the route.
	status, body = do(t, secondBoot, http.MethodGet, staleHref, deviceLFDIB)
	if status != http.StatusOK {
		t.Errorf("owner %s GET %s: status %d, want 200; body=%s", deviceLFDIB, staleHref, status, body)
	}
}

// assertNoRegistrationLeak asserts a denied response carries none of the
// resource it denied: not the PIN, not an XML document, not the owning
// device's identity. A 403 that still writes the body is not a gate.
func assertNoRegistrationLeak(t *testing.T, body string, pin uint32) {
	t.Helper()
	for _, leak := range []struct {
		what  string
		found bool
	}{
		{"the Registration PIN", strings.Contains(body, itoa(pin))},
		{"a Registration document", strings.Contains(body, "<Registration")},
		{"any XML document", strings.Contains(body, "<?xml")},
		{"the owning device's LFDI", strings.Contains(body, deviceLFDIA) || strings.Contains(body, deviceLFDIB)},
	} {
		if leak.found {
			t.Errorf("denied response leaked %s; body=%q", leak.what, body)
		}
	}
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var buf [10]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// TestURLIndex_DeviceKeepsIndexAcrossRestart asserts the end-to-end stability
// property through the served hrefs, not just at the allocator API: a device
// that re-registers after a restart against the same persisted index state
// gets the same URLs back.
func TestURLIndex_DeviceKeepsIndexAcrossRestart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "edevindex.json")

	before, _ := indexTestServer(t, path)
	wantA := register(t, before, deviceLFDIA)
	wantB := register(t, before, deviceLFDIB)

	// Restart: a new server and new stores, sharing only the persisted index
	// file. Registering in the OPPOSITE order proves the index comes from the
	// persisted assignment rather than from provisioning order.
	after, _ := indexTestServer(t, path)
	gotB := register(t, after, deviceLFDIB)
	gotA := register(t, after, deviceLFDIA)

	if gotA.Href != wantA.Href {
		t.Errorf("device A href moved across restart: was %q, now %q", wantA.Href, gotA.Href)
	}
	if gotB.Href != wantB.Href {
		t.Errorf("device B href moved across restart: was %q, now %q", wantB.Href, gotB.Href)
	}
	if gotA.RegistrationLink.Href != wantA.RegistrationLink.Href {
		t.Errorf("device A RegistrationLink moved across restart: was %q, now %q",
			wantA.RegistrationLink.Href, gotA.RegistrationLink.Href)
	}
	if gotA.Href == gotB.Href {
		t.Errorf("both devices resolved to the same href %q after restart", gotA.Href)
	}
}
