package server_test

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// One browser page load of the admin shell is four authenticated requests:
// the document, its script, its stylesheet and its icon. They arrive on one
// admin_ticket cookie, so the whole set has to be admitted by that single
// credential. These tests drive the real admin router from a non-loopback
// address, which is the only shape where the credential is actually checked.

const (
	testUIHost = "example.com"

	// spaMountPoint is the element the built bundle mounts into. Its presence
	// in a body is what distinguishes the shell from anything else served with
	// the same status.
	spaMountPoint = `<div id="app">`

	// browserNavigationAccept is the Accept header a current browser sends on a
	// top-level document request.
	browserNavigationAccept = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"
)

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newUIRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Host = testUIHost
	if isLoopbackRemoteAddr(req.RemoteAddr) {
		t.Fatalf("fixture RemoteAddr %q is loopback: the request would take the Path 0 bypass and assert nothing about the credential", req.RemoteAddr)
	}
	return req
}

func newUIRouter(t *testing.T) (http.Handler, *auth.SessionStore) {
	t.Helper()
	sessions := auth.NewSessionStore(30*time.Minute, 8*time.Hour)
	router, _ := server.BuildAdminRouter(
		"the-key", nil, newTestStores(), "GCM",
		auth.NewTicketStore(30*time.Second), sessions,
		[]string{testUIHost}, false,
	)
	return router, sessions
}

// loginForCookie performs the browser's first step and returns the cookie the
// server minted, with its security flags asserted at the point they are set.
func loginForCookie(t *testing.T, router http.Handler) *http.Cookie {
	t.Helper()
	form := url.Values{"key": []string{"the-key"}}
	req := newUIRequest(t, http.MethodPost, "/auth/login", form.Encode())
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("POST /auth/login: status = %d, want 303; body = %q", rec.Code, rec.Body.String())
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name != auth.AdminTicketCookieName {
			continue
		}
		if c.Value == "" {
			t.Fatal("login set an empty admin_ticket cookie")
		}
		if !c.HttpOnly {
			t.Error("admin_ticket cookie must be HttpOnly")
		}
		if !c.Secure {
			t.Error("admin_ticket cookie must be Secure")
		}
		if c.SameSite != http.SameSiteStrictMode {
			t.Errorf("admin_ticket cookie SameSite = %v, want Strict", c.SameSite)
		}
		return c
	}
	t.Fatalf("login response set no %s cookie: %v", auth.AdminTicketCookieName, rec.Result().Cookies())
	return nil
}

// builtAssetPaths returns the request paths for the committed bundle's script
// and stylesheet. The filenames carry a content hash, so they are read from
// the tree rather than written down here.
func builtAssetPaths(t *testing.T) (js, css string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("web", "dist", "assets"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".js":
			js = path.Join("/ui/assets", e.Name())
		case ".css":
			css = path.Join("/ui/assets", e.Name())
		}
	}
	if js == "" || css == "" {
		t.Fatalf("committed bundle is missing a script or stylesheet: js=%q css=%q", js, css)
	}
	return js, css
}

func mustReadDist(t *testing.T, requestPath string) []byte {
	t.Helper()
	rel := strings.TrimPrefix(requestPath, "/ui/")
	b, err := os.ReadFile(filepath.Join("web", "dist", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAdminUIShellPageLoadUnderOneCookieSession is the end-to-end gate: one
// login, then every request a single page load makes, each asserted for the
// asset it actually returned. Status alone would not prove it, because
// index.html served in place of the script is a 200 and still a blank shell.
func TestAdminUIShellPageLoadUnderOneCookieSession(t *testing.T) {
	router, _ := newUIRouter(t)
	cookie := loginForCookie(t, router)

	jsPath, cssPath := builtAssetPaths(t)
	cases := []struct {
		path        string
		contentType string
		wantBytes   bool
	}{
		{path: "/ui/", contentType: "text/html"},
		{path: jsPath, contentType: "javascript", wantBytes: true},
		{path: cssPath, contentType: "text/css", wantBytes: true},
		{path: "/ui/favicon.svg", contentType: "image/svg+xml", wantBytes: true},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := newUIRequest(t, http.MethodGet, tc.path, "")
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s on one cookie session: status = %d, want 200; body = %q", tc.path, rec.Code, rec.Body.String())
			}
			ct := rec.Header().Get("Content-Type")
			if !strings.Contains(ct, tc.contentType) {
				t.Errorf("GET %s Content-Type = %q, want it to contain %q", tc.path, ct, tc.contentType)
			}
			if tc.wantBytes {
				if got, want := rec.Body.Bytes(), mustReadDist(t, tc.path); string(got) != string(want) {
					t.Errorf("GET %s returned %d bytes, want the %d bytes of the committed asset", tc.path, len(got), len(want))
				}
				if strings.Contains(rec.Body.String(), spaMountPoint) {
					t.Errorf("GET %s returned the SPA document instead of the asset", tc.path)
				}
			} else if !strings.Contains(rec.Body.String(), spaMountPoint) {
				t.Errorf("GET %s does not look like the built index.html; body = %q", tc.path, rec.Body.String())
			}
		})
	}
}

// TestAdminUIShellRefusedWithoutCredential is the control for the test above.
// Without it, a fixture that had slipped back onto the loopback bypass would
// pass identically, which is how this defect stayed hidden.
//
// The property under test is that the shell is never served without a
// credential. What a refusal LOOKS like depends on who asked: a browser
// navigating to the document is redirected to the login form, and the
// subresources that document pulls keep the JSON status they can read. Both
// answers are asserted on the body, because neither status proves the bundle
// was withheld.
func TestAdminUIShellRefusedWithoutCredential(t *testing.T) {
	router, _ := newUIRouter(t)
	jsPath, cssPath := builtAssetPaths(t)

	// "/" is in the navigation list because the dashboard at the bare root is
	// now the SPA, served from the same embedded bundle as the paths below and
	// needing the same credential. newUIRequest fails the test outright if the
	// fixture address is loopback, which would take the Path 0 bypass and
	// assert nothing.
	navigations := []string{"/", "/ui/"}
	subresources := []string{jsPath, cssPath, "/ui/favicon.svg"}

	for _, p := range navigations {
		t.Run("navigation "+p, func(t *testing.T) {
			req := newUIRequest(t, http.MethodGet, p, "")
			req.Header.Set("Sec-Fetch-Dest", "document")
			req.Header.Set("Sec-Fetch-Mode", "navigate")
			req.Header.Set("Accept", browserNavigationAccept)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			// The body is checked first and on its own: a leak that carried
			// the right status would otherwise be masked by the status check
			// failing the subtest before this ran.
			if strings.Contains(rec.Body.String(), spaMountPoint) {
				t.Errorf("unauthenticated navigation to %s returned the shell: body contains %s", p, spaMountPoint)
			}
			if rec.Code != http.StatusSeeOther {
				t.Errorf("unauthenticated non-loopback navigation to %s: status = %d, want 303; body = %q", p, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Location"); got != "/login" {
				t.Errorf("unauthenticated navigation to %s: Location = %q, want %q", p, got, "/login")
			}
		})
	}

	for _, p := range subresources {
		t.Run("subresource "+p, func(t *testing.T) {
			req := newUIRequest(t, http.MethodGet, p, "")
			req.Header.Set("Sec-Fetch-Dest", "script")
			req.Header.Set("Accept", "*/*")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if strings.Contains(rec.Body.String(), spaMountPoint) {
				t.Errorf("unauthenticated GET %s returned the shell: body contains %s", p, spaMountPoint)
			}
			if got, want := rec.Body.String(), `{"error":"admin authentication required"}`; got != want {
				t.Errorf("unauthenticated GET %s body = %q, want %q", p, got, want)
			}
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("unauthenticated non-loopback GET %s: status = %d, want 401; body = %q", p, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAdminUIShellSetsNoCookiePerRequest pins that serving the shell does not
// rotate the session cookie: a rotation cannot survive the parallel loads
// above, and a browser left holding a retired value drops to /login.
func TestAdminUIShellSetsNoCookiePerRequest(t *testing.T) {
	router, _ := newUIRouter(t)
	cookie := loginForCookie(t, router)

	req := newUIRequest(t, http.MethodGet, "/ui/", "")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/ with a valid cookie: status = %d, want 200", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.AdminTicketCookieName {
			t.Errorf("serving the shell re-set %s=%q; only the login handler may set it", c.Name, c.Value)
		}
	}
}

// TestAdminUIShellDisallowedHostRefusedBeforeAuth keeps the ordering proof
// that the host gate sits outside the credential check: a wrong Host is
// refused whether or not a valid cookie is presented.
func TestAdminUIShellDisallowedHostRefusedBeforeAuth(t *testing.T) {
	router, _ := newUIRouter(t)
	cookie := loginForCookie(t, router)

	for _, tc := range []struct {
		name   string
		cookie *http.Cookie
	}{
		{name: "with a valid cookie", cookie: cookie},
		{name: "with no credential", cookie: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := newUIRequest(t, http.MethodGet, "/ui/", "")
			req.Host = "evil.example.net"
			if tc.cookie != nil {
				req.AddCookie(tc.cookie)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusMisdirectedRequest {
				t.Fatalf("GET /ui/ with a disallowed Host %s: status = %d, want 421; body = %q", tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}
