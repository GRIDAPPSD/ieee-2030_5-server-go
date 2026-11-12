package assembly_test

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/routefamily"
)

// aclPolicy puts the production ACL in front of the protocol chain.
func aclPolicy() assembly.AuthPolicy {
	pol := testAuthPolicy()
	pol.Wrap = auth.ACLMiddleware(auth.DefaultACLRules())
	return pol
}

func clientRequest(method, path string, withCert bool) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	if withCert {
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
	}
	return req
}

// Every mounted pattern's method must be allowed by its family row, and every
// family row must have a mount that carries it.
func TestRouteFamilyTableCoversMountedPatterns(t *testing.T) {
	t.Parallel()

	_, patterns := assembly.BuildProtocolRouter(assembly.RouterConfig{}, testStores(), aclPolicy(), "s", "l", nil)
	families := routefamily.All()

	for _, p := range patterns {
		method, path, ok := strings.Cut(p, " ")
		if !ok {
			method, path = "", p
		}
		var fam *routefamily.Family
		for i := range families {
			if path == families[i].Prefix || strings.HasPrefix(path, families[i].Prefix+"/") {
				fam = &families[i]
			}
		}
		if fam == nil {
			t.Errorf("pattern %q is under no route family", p)
			continue
		}
		if method != "" && !slices.Contains(fam.Methods, method) {
			t.Errorf("pattern %q mounts %s, which family %s does not allow", p, method, fam.Prefix)
		}
		if path != fam.Prefix && !fam.Subtree {
			t.Errorf("pattern %q is beneath family %s, which mounts the bare path only", p, fam.Prefix)
		}
	}

	rules := auth.DefaultACLRules()
	mounts := routefamily.Mounts()
	for _, r := range rules {
		if !slices.Contains(mounts, r.PathPrefix) {
			t.Errorf("ACL row %s has no mount", r.PathPrefix)
		}
	}
	if len(rules) != len(families) {
		t.Errorf("ACL rows = %d, families = %d", len(rules), len(families))
	}
	for _, f := range families {
		if !slices.Contains(f.Methods, http.MethodGet) {
			continue
		}
		if !slices.Contains(f.Methods, http.MethodHead) {
			t.Errorf("family %s allows GET but not HEAD", f.Prefix)
		}
	}
}

// A request under a path no family names is refused with 403 before any
// handler, for a client with no TLS identity, one with a device certificate,
// and one the router has identified as a manager. The activity recorder sits
// inside the chain, so a request that reached the handler side would be
// recorded.
func TestUnnamedPathsAreRefusedBeforeAnyHandler(t *testing.T) {
	t.Parallel()

	paths := []string{"/", "/zzz", "/dcap/x", "/tm/x", "/edevx", "/dcapx", "/admin", "/ntfy/x"}
	kinds := []struct {
		name string
		cert bool
	}{{"unauthenticated TLS", false}, {"device", true}, {"manager", true}}

	for _, kind := range kinds {
		for _, path := range paths {
			rec := activity.New()
			pol := aclPolicy()
			h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{Activity: rec}, testStores(), pol, "s", "l", nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, clientRequest(http.MethodGet, path, kind.cert))
			if w.Code != http.StatusForbidden {
				t.Errorf("%s GET %s: status = %d, want 403", kind.name, path, w.Code)
			}
			if _, n, ok := rec.Last(testLFDI); ok || n != 0 {
				t.Errorf("%s GET %s: a handler-side observer ran (count %d)", kind.name, path, n)
			}
			if w.Code == http.StatusForbidden && !strings.Contains(w.Body.String(), routefamily.RefusalBody) {
				t.Errorf("%s GET %s: body = %q, want the route-family refusal text", kind.name, path, w.Body.String())
			}
		}
	}

	// Control: a named path under the same wiring reaches the observer.
	rec := activity.New()
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{Activity: rec}, testStores(), aclPolicy(), "s", "l", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, clientRequest(http.MethodGet, "/dcap", true))
	if w.Code != http.StatusOK {
		t.Fatalf("control GET /dcap: status = %d, want 200", w.Code)
	}
	if _, n, ok := rec.Last(testLFDI); !ok || n != 1 {
		t.Errorf("control GET /dcap was not observed: count %d ok=%v", n, ok)
	}
}

// With no Wrap at all the outer mux still refuses an unnamed path.
func TestUnnamedPathIsRefusedWithoutWrap(t *testing.T) {
	t.Parallel()
	pol := testAuthPolicy()
	pol.Wrap = nil
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, testStores(), pol, "s", "l", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/zzz", nil))
	if w.Code != http.StatusForbidden {
		t.Errorf("GET /zzz with nil Wrap: status = %d, want 403", w.Code)
	}
}

// PUT and DELETE on a mirror usage point instance pass the production ACL and
// reach their handlers.
func TestMirrorUsagePointPutDeleteReachHandlersThroughACL(t *testing.T) {
	t.Parallel()

	stores := testStores()
	h, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, stores, aclPolicy(), "s", "l", nil)

	body := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>FAMILY_MRID</mRID></MirrorUsagePoint>`
	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{{}}}
		req.Header.Set("Content-Type", "application/sep+xml")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	post := do(http.MethodPost, "/mup")
	if post.Code != http.StatusCreated {
		t.Fatalf("POST /mup: status = %d, want 201; body = %s", post.Code, post.Body.String())
	}
	path := post.Header().Get("Location")
	if path == "" {
		t.Fatal("POST /mup returned no Location")
	}
	if w := do(http.MethodPut, path); w.Code != http.StatusNoContent {
		t.Errorf("PUT %s: status = %d, want 204; body = %s", path, w.Code, w.Body.String())
	}
	if w := do(http.MethodDelete, path); w.Code != http.StatusOK {
		t.Errorf("DELETE %s: status = %d, want 200; body = %s", path, w.Code, w.Body.String())
	}
	if w := do(http.MethodGet, path); w.Code != http.StatusNotFound {
		t.Errorf("GET %s after DELETE: status = %d, want 404", path, w.Code)
	}
}
