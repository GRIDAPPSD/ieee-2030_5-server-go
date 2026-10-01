package assembly_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// Production wiring for #720: registerMirrorRoutes threads
// stores.EndDeviceManagers into all five /mup handler constructors
// (assembly.go, "POST /mup" through "DELETE /mup/{id}"). Every other /mup
// test in this package drives BuildProtocolRouter with testAuthPolicy's
// single fixed identity, which cannot distinguish a manager from a device;
// this file is the one place that proves the wiring itself, not the rule the
// metering package already covers with a direct (non-router) construction.

const (
	wiringManagerLFDI = "7777777777777777777777777777777777777777"
	wiringDeviceLFDI  = "8888888888888888888888888888888888888888"
)

// wiringCallerHeader carries the caller's LFDI the way gateTestPolicy already
// establishes for the /edev ownership-gate tests: a header standing in for
// the certificate, read back by AuthPolicy.Identity from the request
// context. Reused here rather than invented again, so both suites agree on
// what "identity carried the way production carries it" means.
func mupWiringRouter(t *testing.T) (*httptest.Server, *assembly.Stores) {
	t.Helper()
	stores := testStores()
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv, stores
}

func mupWiringRequest(t *testing.T, method, url, caller, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatalf("build %s %s: %v", method, url, err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/sep+xml")
	}
	req.Header.Set(gateIdentityHeader, caller)
	resp, err := isolatedClient().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return resp
}

// TestAssembly_MirrorManagerWiringThroughTheRouter proves the production
// wiring end to end: a manager, assigned through the real
// store.EndDeviceManagementStore BuildProtocolRouter is given, creates a
// mirror naming a managed device, and the device itself (a DIFFERENT
// identity from the creator) can then read that same mirror. Neither would
// be possible if registerMirrorRoutes still passed a nil managers reader to
// any of the five /mup constructors.
func TestAssembly_MirrorManagerWiringThroughTheRouter(t *testing.T) {
	t.Parallel()
	srv, stores := mupWiringRouter(t)

	if err := stores.EndDeviceManagers.Assign(context.Background(), wiringManagerLFDI, wiringDeviceLFDI); err != nil {
		t.Fatalf("assign manager: %v", err)
	}

	body := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>WIRING_MUP</mRID>` +
		`<deviceLFDI>` + wiringDeviceLFDI + `</deviceLFDI></MirrorUsagePoint>`
	createResp := mupWiringRequest(t, http.MethodPost, srv.URL+"/mup", wiringManagerLFDI, body)
	_ = createResp.Body.Close()
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("manager POST /mup for a managed device: status = %d, want 201", createResp.StatusCode)
	}
	loc := createResp.Header.Get("Location")
	if loc == "" {
		t.Fatal("POST /mup returned no Location header")
	}

	// The device itself, a different identity from the creator, reads the
	// mirror the manager created for it.
	getResp := mupWiringRequest(t, http.MethodGet, srv.URL+loc, wiringDeviceLFDI, "")
	getBody, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("device GET %s: status = %d, want 200; body = %s", loc, getResp.StatusCode, getBody)
	}
	if !strings.Contains(string(getBody), "<deviceLFDI>"+wiringDeviceLFDI+"</deviceLFDI>") {
		t.Errorf("served body does not carry the managed device's LFDI; body = %s", getBody)
	}
}

// TestAssembly_MirrorUnmanagedClaimRefusedThroughTheRouter is the negative
// control for the same wiring: with no management pair assigned, the same
// request is refused. Without this, the positive test above could pass for
// the wrong reason (a wiring bug that admits everyone).
func TestAssembly_MirrorUnmanagedClaimRefusedThroughTheRouter(t *testing.T) {
	t.Parallel()
	srv, _ := mupWiringRouter(t)

	body := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>WIRING_REFUSED</mRID>` +
		`<deviceLFDI>` + wiringDeviceLFDI + `</deviceLFDI></MirrorUsagePoint>`
	resp := mupWiringRequest(t, http.MethodPost, srv.URL+"/mup", wiringManagerLFDI, body)
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unmanaged claim through the router: status = %d, want 403; body = %s", resp.StatusCode, respBody)
	}
}

// mupWiringInstanceRoutes is every /mup route BESIDES POST /mup (the two
// tests above already cover creation): the five separate HandleFunc calls in
// registerMirrorRoutes (assembly.go), each its own line passing
// stores.EndDeviceManagers, so a mutation on any ONE line is a defect this
// table must catch on that line's own row. path is relative to the created
// mirror's Location; wantStatus is the response on the admitted case.
var mupWiringInstanceRoutes = []struct {
	name       string
	method     string
	path       func(loc string) string
	body       func(mrid string) string
	wantStatus int
}{
	{
		name:       "GET /mup/{id}",
		method:     http.MethodGet,
		path:       func(loc string) string { return loc },
		body:       func(string) string { return "" },
		wantStatus: http.StatusOK,
	},
	{
		name:   "PUT /mup/{id}",
		method: http.MethodPut,
		path:   func(loc string) string { return loc },
		body: func(mrid string) string {
			return `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>` + mrid + `</mRID></MirrorUsagePoint>`
		},
		wantStatus: http.StatusNoContent,
	},
	{
		name:   "POST /mup/{id}",
		method: http.MethodPost,
		path:   func(loc string) string { return loc },
		body: func(string) string {
			return `<MirrorMeterReading xmlns="urn:ieee:std:2030.5:ns"><mRID>WIRING_MMR_ID</mRID></MirrorMeterReading>`
		},
		wantStatus: http.StatusCreated,
	},
	{
		name:   "POST /mup/{id}/mr",
		method: http.MethodPost,
		path:   func(loc string) string { return loc + "/mr" },
		body: func(string) string {
			return `<MirrorMeterReading xmlns="urn:ieee:std:2030.5:ns"><mRID>WIRING_MMR_MR</mRID></MirrorMeterReading>`
		},
		wantStatus: http.StatusCreated,
	},
	{
		// DELETE last: it removes the resource, so within one subtest's own
		// server this must run after any read/write case that needs the
		// record present. Each route below runs in its OWN subtest with its
		// OWN router, so ordering across routes does not matter; only
		// mattering here because it documents why this entry names no
		// further requests after itself.
		name:       "DELETE /mup/{id}",
		method:     http.MethodDelete,
		path:       func(loc string) string { return loc },
		body:       func(string) string { return "" },
		wantStatus: http.StatusOK,
	},
}

// mupWiringCreateAsManager creates a mirror for wiringDeviceLFDI, POSTed by
// creator (who must already be assigned as the device's manager, or must BE
// the device), and returns its Location.
func mupWiringCreateAsManager(t *testing.T, srv *httptest.Server, creator, mrid string) string {
	t.Helper()
	body := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>` + mrid + `</mRID>` +
		`<deviceLFDI>` + wiringDeviceLFDI + `</deviceLFDI></MirrorUsagePoint>`
	resp := mupWiringRequest(t, http.MethodPost, srv.URL+"/mup", creator, body)
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed create as %q: status = %d, want 201; body = %s", creator, resp.StatusCode, respBody)
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		t.Fatal("seed create: no Location header")
	}
	return loc
}

// TestAssembly_MirrorInstanceRoutesManagerWiring is the MEDIUM fix (#720
// round 2): TestAssembly_MirrorManagerWiringThroughTheRouter above proved
// only POST /mup and a GET as the DEVICE ITSELF, which cannot tell a wired
// managers reader from a nil one on the other four registrations, because
// self-access needs no management store at all. This test drives every
// remaining /mup instance route through the real router with a genuine
// manager identity, admitted and refused, so a nil passed at any one of the
// five registerMirrorRoutes call sites (assembly.go: GET, POST /mup/{id},
// POST /mup/{id}/mr, PUT, DELETE) is caught on that route's own subtest.
func TestAssembly_MirrorInstanceRoutesManagerWiring(t *testing.T) {
	t.Parallel()
	for _, rt := range mupWiringInstanceRoutes {
		rt := rt
		t.Run(rt.name+"/manager admitted", func(t *testing.T) {
			t.Parallel()
			srv, stores := mupWiringRouter(t)
			const mrid = "WIRING_INSTANCE"
			if err := stores.EndDeviceManagers.Assign(context.Background(), wiringManagerLFDI, wiringDeviceLFDI); err != nil {
				t.Fatalf("assign manager: %v", err)
			}
			loc := mupWiringCreateAsManager(t, srv, wiringManagerLFDI, mrid)

			resp := mupWiringRequest(t, rt.method, srv.URL+rt.path(loc), wiringManagerLFDI, rt.body(mrid))
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != rt.wantStatus {
				t.Fatalf("manager %s %s: status = %d, want %d; body = %s", rt.method, rt.path(loc), resp.StatusCode, rt.wantStatus, respBody)
			}
		})
		t.Run(rt.name+"/unassigned manager refused", func(t *testing.T) {
			t.Parallel()
			srv, stores := mupWiringRouter(t)
			const mrid = "WIRING_INSTANCE"
			// Someone has to be able to create the seed mirror: a second
			// identity, assigned as the device's manager, does that. The
			// route under test then runs as wiringManagerLFDI, which this
			// store never assigns to anything, so admission can only come
			// from a wiring bug.
			const seedCreator = "9999999999999999999999999999999999999999"
			if err := stores.EndDeviceManagers.Assign(context.Background(), seedCreator, wiringDeviceLFDI); err != nil {
				t.Fatalf("assign seed creator: %v", err)
			}
			loc := mupWiringCreateAsManager(t, srv, seedCreator, mrid)

			resp := mupWiringRequest(t, rt.method, srv.URL+rt.path(loc), wiringManagerLFDI, rt.body(mrid))
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("unassigned manager %s %s: status = %d, want 403; body = %s", rt.method, rt.path(loc), resp.StatusCode, respBody)
			}
		})
	}
}
