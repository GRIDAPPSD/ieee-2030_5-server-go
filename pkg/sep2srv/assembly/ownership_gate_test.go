package assembly_test

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// Ownership gate on every /edev/{id}-scoped route.
//
// Every request here goes through BuildProtocolRouter with identity carried
// the way production carries it: Wrap puts the certificate-derived identity
// into the request context and AuthPolicy.Identity reads it back. The caller's
// identity is never taken from the URL.

const (
	// gateIdentityHeader stands in for the client certificate. Absent means
	// no identity at all (ok=false).
	gateIdentityHeader = "X-Test-Caller-LFDI"
	// gateEmptyIdentityHeader asks for ok=true with an empty LFDI, a state
	// no real certificate produces and the gate must still refuse.
	gateEmptyIdentityHeader = "X-Test-Caller-Empty-LFDI"

	victimID   = "1"
	victimLFDI = deviceLFDIA
	victimSFDI = deviceSFDIA
	callerID   = "2"
	callerLFDI = deviceLFDIB
	callerSFDI = deviceSFDIB
)

type gateCtxKey struct{}

// gateTestPolicy returns an AuthPolicy whose identity is request-scoped, so one
// server can answer as the victim, as another device, and as nobody.
func gateTestPolicy() assembly.AuthPolicy {
	p := testAuthPolicy()
	p.Wrap = func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Header.Get(gateEmptyIdentityHeader) != "":
				r = r.WithContext(context.WithValue(r.Context(), gateCtxKey{}, callerIdentity{}))
			case r.Header.Get(gateIdentityHeader) != "":
				lfdi := r.Header.Get(gateIdentityHeader)
				r = r.WithContext(context.WithValue(r.Context(), gateCtxKey{}, callerIdentity{lfdi: lfdi, sfdi: sfdiFor(lfdi)}))
			}
			h.ServeHTTP(w, r)
		})
	}
	p.Identity = identityFromGateContext
	return p
}

func identityFromGateContext(ctx context.Context) (string, string, bool) {
	id, ok := ctx.Value(gateCtxKey{}).(callerIdentity)
	if !ok {
		return "", "", false
	}
	return id.lfdi, id.sfdi, true
}

func sfdiFor(lfdi string) string {
	switch lfdi {
	case victimLFDI:
		return victimSFDI
	case callerLFDI:
		return callerSFDI
	default:
		return "9999999999999999"
	}
}

// seedDevice writes an EndDevice straight into the raw store, the boot-seeding
// path, so the record's stored LFDI is exactly what the test names.
func seedDevice(t *testing.T, devs store.EndDeviceStore, id, lfdi, sfdi string) sep2.EndDevice {
	t.Helper()
	enabled := true
	dev := sep2.EndDevice{LFDI: lfdi, SFDI: sfdi, ChangedTime: 1600000000, Enabled: &enabled}
	dev.Href = "/edev/" + id
	if err := devs.Create(context.Background(), id, dev); err != nil {
		t.Fatalf("seed EndDevice %q: %v", id, err)
	}
	return dev
}

// seedOwnedDevices seeds, at each id not already present, an EndDevice owned by
// the default test identity, so a test about a route's own behavior is not
// refused by the ownership gate before that behavior runs. The SFDI is left
// empty so a later POST /edev by the same identity still registers anew.
func seedOwnedDevices(t *testing.T, devs store.EndDeviceStore, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if _, err := devs.Get(context.Background(), id); err == nil {
			continue
		}
		seedDevice(t, devs, id, testLFDI, "")
	}
}

// gateServer builds a router over stores seeded with a victim and a caller.
func gateServer(t *testing.T, stores *assembly.Stores, policy assembly.AuthPolicy) *httptest.Server {
	t.Helper()
	handler, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, policy, "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func seededGateStores(t *testing.T) *assembly.Stores {
	t.Helper()
	stores := testStores()
	seedDevice(t, stores.EndDevices, victimID, victimLFDI, victimSFDI)
	seedDevice(t, stores.EndDevices, callerID, callerLFDI, callerSFDI)
	return stores
}

// gateRequest sends one request. asLFDI "" sends no identity at all.
func gateRequest(t *testing.T, srv *httptest.Server, method, path, asLFDI, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build %s %s: %v", method, path, err)
	}
	if asLFDI != "" {
		req.Header.Set(gateIdentityHeader, asLFDI)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/sep+xml")
	}
	return sendGateRequest(t, req)
}

func sendGateRequest(t *testing.T, req *http.Request) (int, []byte) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body of %s %s: %v", req.Method, req.URL.Path, err)
	}
	return resp.StatusCode, raw
}

// assertDenialLeaksNothing checks the raw bytes of a refusal: no sep2 document
// and no identity field of the victim.
func assertDenialLeaksNothing(t *testing.T, label string, raw []byte) {
	t.Helper()
	body := string(raw)
	// A refusal carries one of the fixed bodies, so no stored field can reach
	// the client, including those no other assertion names.
	switch body {
	case "forbidden\n", "not found\n", srverr.DefaultMessage + "\n":
	default:
		t.Errorf("%s: denial body is not one of the fixed refusal bodies; body=%q", label, body)
	}
	for _, forbidden := range []string{victimLFDI, victimSFDI, "1600000000", "urn:ieee:std:2030.5:ns", "<EndDevice", "<?xml"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("%s: denial body carries %q; body=%q", label, forbidden, body)
		}
	}
	var anyDoc struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(raw, &anyDoc); err == nil {
		t.Errorf("%s: denial body parses as an XML document <%s>; body=%q", label, anyDoc.XMLName.Local, body)
	}
}

func TestOwnershipGate_NonOwnerGETIsRefused(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, seededGateStores(t), gateTestPolicy())

	status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, callerLFDI, "")
	if status != http.StatusForbidden {
		t.Fatalf("non-owner GET /edev/%s: status %d, want 403; body=%s", victimID, status, raw)
	}
	assertDenialLeaksNothing(t, "non-owner GET", raw)

	status, raw = gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, victimLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("owner GET /edev/%s: status %d, want 200; body=%s", victimID, status, raw)
	}
	var dev sep2.EndDevice
	if err := xml.Unmarshal(raw, &dev); err != nil {
		t.Fatalf("owner GET body does not decode: %v; body=%s", err, raw)
	}
	if dev.LFDI != victimLFDI {
		t.Errorf("owner GET served LFDI %q, want %q", dev.LFDI, victimLFDI)
	}
}

func TestOwnershipGate_NonOwnerPUTLeavesTheVictimUnchanged(t *testing.T) {
	t.Parallel()
	stores := seededGateStores(t)
	srv := gateServer(t, stores, gateTestPolicy())

	before, err := stores.EndDevices.Get(context.Background(), victimID)
	if err != nil {
		t.Fatalf("read victim before: %v", err)
	}

	forged := `<EndDevice xmlns="urn:ieee:std:2030.5:ns"><changedTime>1</changedTime>` +
		`<lFDI>` + callerLFDI + `</lFDI><sFDI>` + callerSFDI + `</sFDI></EndDevice>`
	status, raw := gateRequest(t, srv, http.MethodPut, "/edev/"+victimID, callerLFDI, forged)
	if status != http.StatusForbidden {
		t.Errorf("non-owner PUT /edev/%s: status %d, want 403; body=%s", victimID, status, raw)
	}
	assertDenialLeaksNothing(t, "non-owner PUT", raw)

	after, err := stores.EndDevices.Get(context.Background(), victimID)
	if err != nil {
		t.Fatalf("victim is gone after a refused PUT: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("victim record changed by a refused PUT:\nbefore=%+v\nafter =%+v", before, after)
	}
	if after.LFDI != victimLFDI || after.SFDI != victimSFDI || after.ChangedTime != 1600000000 {
		t.Errorf("victim identity fields changed: LFDI=%q SFDI=%q changedTime=%d", after.LFDI, after.SFDI, after.ChangedTime)
	}
}

func TestOwnershipGate_NonOwnerDELETELeavesTheVictimPresent(t *testing.T) {
	t.Parallel()
	stores := seededGateStores(t)
	srv := gateServer(t, stores, gateTestPolicy())

	status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, callerLFDI, "")
	if status != http.StatusForbidden {
		t.Errorf("non-owner DELETE /edev/%s: status %d, want 403; body=%s", victimID, status, raw)
	}
	assertDenialLeaksNothing(t, "non-owner DELETE", raw)

	dev, err := stores.EndDevices.Get(context.Background(), victimID)
	if err != nil {
		t.Fatalf("victim deleted by a refused DELETE: %v", err)
	}
	if dev.LFDI != victimLFDI {
		t.Errorf("victim LFDI after refused DELETE = %q, want %q", dev.LFDI, victimLFDI)
	}

	status, raw = gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, victimLFDI, "")
	if status != http.StatusNoContent {
		t.Fatalf("owner DELETE /edev/%s: status %d, want 204; body=%s", victimID, status, raw)
	}
	if _, err := stores.EndDevices.Get(context.Background(), victimID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("owner DELETE did not remove the record: err=%v", err)
	}
}

func TestOwnershipGate_NonOwnerNestedWriteIsRefused(t *testing.T) {
	t.Parallel()
	stores := seededGateStores(t)
	srv := gateServer(t, stores, gateTestPolicy())

	body := `<DERSettings xmlns="urn:ieee:std:2030.5:ns"><updatedTime>1</updatedTime></DERSettings>`
	status, raw := gateRequest(t, srv, http.MethodPut, "/edev/"+victimID+"/der/x/derg", callerLFDI, body)
	if status != http.StatusForbidden {
		t.Errorf("non-owner PUT /edev/%s/der/x/derg: status %d, want 403; body=%s", victimID, status, raw)
	}
	assertDenialLeaksNothing(t, "non-owner nested PUT", raw)

	if _, err := stores.DERSettings.Get(context.Background(), victimID, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("refused nested PUT wrote DERSettings under the victim: err=%v", err)
	}
}

// wildcard matches one {name} segment of a mux pattern.
var wildcard = regexp.MustCompile(`\{[^}]+\}`)

// gatedPatterns splits the mounted pattern list into the /edev patterns the
// gate must wrap and the two it must leave alone.
func gatedPatterns(t *testing.T, patterns []string) (gated []string) {
	t.Helper()
	exempt := map[string]bool{"GET /edev": true, "POST /edev": true}
	seenExempt := 0
	for _, p := range patterns {
		_, path, ok := strings.Cut(p, " ")
		if !ok {
			t.Fatalf("pattern %q has no method", p)
		}
		if path != "/edev" && !strings.HasPrefix(path, "/edev/") {
			continue
		}
		if exempt[p] {
			seenExempt++
			continue
		}
		gated = append(gated, p)
	}
	if seenExempt != len(exempt) {
		t.Fatalf("expected both exempt patterns mounted, saw %d of %d in %v", seenExempt, len(exempt), patterns)
	}
	if len(gated) == 0 {
		t.Fatal("no /edev/{id} patterns found: the sweep would pass vacuously")
	}
	return gated
}

func concreteGatePath(pattern string) (method, path string) {
	method, path, _ = strings.Cut(pattern, " ")
	return method, wildcard.ReplaceAllString(path, victimID)
}

func TestOwnershipGate_EveryDeviceScopedRouteRefusesANonOwner(t *testing.T) {
	t.Parallel()
	stores := seededGateStores(t)
	handler, patterns := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	gated := gatedPatterns(t, patterns)
	t.Logf("sweeping %d gated /edev patterns of %d mounted", len(gated), len(patterns))
	for _, p := range gated {
		method, path := concreteGatePath(p)
		status, raw := gateRequest(t, srv, method, path, callerLFDI, "")
		if status != http.StatusForbidden {
			t.Errorf("%s as non-owner: status %d, want 403; body=%q", p, status, raw)
			continue
		}
		assertDenialLeaksNothing(t, p, raw)
	}
}

func TestOwnershipGate_OwnerIsNeverRefusedByTheGate(t *testing.T) {
	t.Parallel()
	stores := seededGateStores(t)
	handler, patterns := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	for _, p := range gatedPatterns(t, patterns) {
		method, path := concreteGatePath(p)
		// DELETE /edev/{id} would remove the device under every later probe.
		if p == "DELETE /edev/{id}" {
			continue
		}
		status, raw := gateRequest(t, srv, method, path, victimLFDI, "")
		if status == http.StatusForbidden || status == http.StatusMethodNotAllowed {
			t.Errorf("%s as owner: status %d; body=%q", p, status, raw)
		}
	}
}

// TestOwnershipGate_DenyMatrix drives GET /edev/{id}/fsa, a route that before
// the gate answered 200 for any id, through every refusal the gate owns.
func TestOwnershipGate_DenyMatrix(t *testing.T) {
	t.Parallel()

	const probe = "/edev/" + victimID + "/fsa"

	type setup struct {
		stores func(t *testing.T) *assembly.Stores
		policy func() assembly.AuthPolicy
		header func(r *http.Request)
		probe  string
	}
	seeded := func(t *testing.T) *assembly.Stores { return seededGateStores(t) }
	asCaller := func(lfdi string) func(r *http.Request) {
		return func(r *http.Request) { r.Header.Set(gateIdentityHeader, lfdi) }
	}
	var typedNil *memory.EndDeviceStore

	cases := []struct {
		name string
		setup
		want int
	}{
		{"owner is allowed", setup{seeded, gateTestPolicy, asCaller(victimLFDI), probe}, http.StatusOK},
		{"other device is refused", setup{seeded, gateTestPolicy, asCaller(callerLFDI), probe}, http.StatusForbidden},
		{"no identity is refused", setup{seeded, gateTestPolicy, func(*http.Request) {}, probe}, http.StatusForbidden},
		{"ok with empty LFDI is refused", setup{seeded, gateTestPolicy, func(r *http.Request) { r.Header.Set(gateEmptyIdentityHeader, "1") }, probe}, http.StatusForbidden},
		{"nil Identity is refused", setup{seeded, func() assembly.AuthPolicy {
			p := gateTestPolicy()
			p.Identity = nil
			return p
		}, asCaller(victimLFDI), probe}, http.StatusForbidden},
		{"nil Wrap is refused", setup{seeded, func() assembly.AuthPolicy {
			p := gateTestPolicy()
			p.Wrap = nil
			return p
		}, asCaller(victimLFDI), probe}, http.StatusForbidden},
		{"zero-value AuthPolicy is refused", setup{seeded, func() assembly.AuthPolicy { return assembly.AuthPolicy{} }, asCaller(victimLFDI), probe}, http.StatusForbidden},
		{"absent device is 404", setup{seeded, gateTestPolicy, asCaller(victimLFDI), "/edev/404/fsa"}, http.StatusNotFound},
		{"stored LFDI empty is refused", setup{func(t *testing.T) *assembly.Stores {
			s := testStores()
			seedDevice(t, s.EndDevices, victimID, "", victimSFDI)
			return s
		}, gateTestPolicy, asCaller(victimLFDI), probe}, http.StatusForbidden},
		{"LFDI compare is case-sensitive", setup{func(t *testing.T) *assembly.Stores {
			s := testStores()
			seedDevice(t, s.EndDevices, victimID, strings.ToLower(victimLFDI), victimSFDI)
			return s
		}, gateTestPolicy, asCaller(victimLFDI), probe}, http.StatusForbidden},
		{"store error is 500", setup{func(t *testing.T) *assembly.Stores {
			s := seededGateStores(t)
			fault := &storetest.Fault{}
			s.EndDevices = storetest.NewFaultyEndDeviceStore(s.EndDevices, fault)
			fault.Arm(storetest.ErrBackendUnavailable)
			return s
		}, gateTestPolicy, asCaller(victimLFDI), probe}, http.StatusInternalServerError},
		{"nil EndDevices is 500", setup{func(t *testing.T) *assembly.Stores {
			s := testStores()
			s.EndDevices = nil
			return s
		}, gateTestPolicy, asCaller(victimLFDI), probe}, http.StatusInternalServerError},
		{"typed-nil EndDevices is 500", setup{func(t *testing.T) *assembly.Stores {
			s := testStores()
			s.EndDevices = typedNil
			return s
		}, gateTestPolicy, asCaller(victimLFDI), probe}, http.StatusInternalServerError},
		{"no identity on an absent device is 403", setup{seeded, gateTestPolicy, func(*http.Request) {}, "/edev/404/fsa"}, http.StatusForbidden},
		{"empty LFDI on an absent device is 403", setup{seeded, gateTestPolicy, func(r *http.Request) { r.Header.Set(gateEmptyIdentityHeader, "1") }, "/edev/404/fsa"}, http.StatusForbidden},
		{"stored LFDI a prefix of the caller's is refused", setup{func(t *testing.T) *assembly.Stores {
			s := testStores()
			seedDevice(t, s.EndDevices, victimID, victimLFDI[:20], victimSFDI)
			return s
		}, gateTestPolicy, asCaller(victimLFDI), probe}, http.StatusForbidden},
		{"caller LFDI a prefix of the stored is refused", setup{seeded, gateTestPolicy, asCaller(victimLFDI[:20]), probe}, http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			srv := gateServer(t, tc.stores(t), tc.policy())
			req, err := http.NewRequest(http.MethodGet, srv.URL+tc.probe, nil)
			if err != nil {
				t.Fatal(err)
			}
			tc.header(req)
			status, raw := sendGateRequest(t, req)
			if status == http.StatusMethodNotAllowed {
				t.Fatalf("gate answered 405; body=%q", raw)
			}
			if status != tc.want {
				t.Fatalf("status %d, want %d; body=%q", status, tc.want, raw)
			}
			if status != http.StatusOK {
				assertDenialLeaksNothing(t, tc.name, raw)
			}
		})
	}
}

// TestOwnershipGate_RegistrationRouteIsWrapped distinguishes the outer gate
// from the in-handler check on /rg. A record stored with no LFDI, read by a
// caller whose identity carries an empty LFDI, passes the handler's own
// comparison; only the gate refuses it.
func TestOwnershipGate_RegistrationRouteIsWrapped(t *testing.T) {
	t.Parallel()
	stores := testStores()
	seedDevice(t, stores.EndDevices, victimID, "", victimSFDI)
	if err := stores.Registrations.Create(context.Background(), victimID, sep2.Registration{PIN: testFixturePIN, DateTimeRegistered: 1600000000}); err != nil {
		t.Fatalf("seed Registration: %v", err)
	}
	srv := gateServer(t, stores, gateTestPolicy())

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/edev/"+victimID+"/rg", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(gateEmptyIdentityHeader, "1")
	status, raw := sendGateRequest(t, req)
	if status != http.StatusForbidden {
		t.Errorf("GET /edev/%s/rg with an empty caller LFDI over a record with no LFDI: status %d, want 403 from the gate; body=%q", victimID, status, raw)
	}
	assertDenialLeaksNothing(t, "empty-identity /rg", raw)
	assertNoRegistrationLeak(t, string(raw), testFixturePIN)
}

// TestOwnershipGate_StoreErrorIsLogged cannot run in parallel: it swaps the
// process-wide log output.
func TestOwnershipGate_StoreErrorIsLogged(t *testing.T) {
	stores := seededGateStores(t)
	fault := &storetest.Fault{}
	stores.EndDevices = storetest.NewFaultyEndDeviceStore(stores.EndDevices, fault)
	srv := gateServer(t, stores, gateTestPolicy())

	buf := &logProbeSafeBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	fault.Arm(storetest.ErrBackendUnavailable)
	status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID+"/fsa", victimLFDI, "")
	if status != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500; body=%q", status, raw)
	}
	assertDenialLeaksNothing(t, "gate store failure", raw)
	captured := buf.String()
	if !containsLineWith(captured, srverr.LogLinePrefix("GET /edev/{id}/fsa"), storetest.ErrBackendUnavailable.Error()) {
		t.Errorf("store failure behind the gate's 500 was not logged with its route and cause; log=%q", captured)
	}
	if strings.Contains(string(raw), storetest.ErrBackendUnavailable.Error()) {
		t.Errorf("store error text reached the client; body=%q", raw)
	}
}

// TestOwnershipGate_IDShapes proves the gate resolves ownership through the
// stored record whatever the id looks like: the index POST /edev allocates, an
// SFDI-prefix id, and an LFDI-shaped id an embedder may seed.
func TestOwnershipGate_IDShapes(t *testing.T) {
	t.Parallel()

	t.Run("index allocated by POST /edev", func(t *testing.T) {
		t.Parallel()
		srv := gateServer(t, testStores(), gateTestPolicy())

		status, raw := gateRequest(t, srv, http.MethodPost, "/edev", victimLFDI, `<EndDevice xmlns="urn:ieee:std:2030.5:ns"/>`)
		if status != http.StatusCreated {
			t.Fatalf("POST /edev: status %d, want 201; body=%s", status, raw)
		}
		var created sep2.EndDevice
		if err := xml.Unmarshal(raw, &created); err != nil {
			t.Fatalf("decode created: %v", err)
		}
		if created.LFDI != victimLFDI || created.Href == "" {
			t.Fatalf("created device LFDI=%q href=%q", created.LFDI, created.Href)
		}

		for _, path := range []string{created.Href, created.Href + "/rg"} {
			if status, raw := gateRequest(t, srv, http.MethodGet, path, victimLFDI, ""); status != http.StatusOK {
				t.Errorf("owner GET %s after self-registration: status %d, want 200; body=%s", path, status, raw)
			}
			status, raw := gateRequest(t, srv, http.MethodGet, path, callerLFDI, "")
			if status != http.StatusForbidden {
				t.Errorf("non-owner GET %s: status %d, want 403; body=%s", path, status, raw)
			}
			assertDenialLeaksNothing(t, "non-owner "+path, raw)
		}
	})

	for name, id := range map[string]string{
		"SFDI-prefix id": victimSFDI[:8],
		"LFDI-shaped id": victimLFDI,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stores := testStores()
			seedDevice(t, stores.EndDevices, id, victimLFDI, victimSFDI)
			srv := gateServer(t, stores, gateTestPolicy())

			if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+id, victimLFDI, ""); status != http.StatusOK {
				t.Errorf("owner GET /edev/%s: status %d, want 200; body=%s", id, status, raw)
			}
			status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+id, callerLFDI, "")
			if status != http.StatusForbidden {
				t.Errorf("non-owner GET /edev/%s: status %d, want 403; body=%s", id, status, raw)
			}
			assertDenialLeaksNothing(t, "non-owner "+name, raw)
		})
	}
}
