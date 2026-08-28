package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// This is the walk documented in docs/admin.md under "Reaching the admin UI in
// a browser from another machine": open the address, get sent to the login
// form, type the key, land on the dashboard, keep navigating. Each step asserts
// what the browser receives, in the order it receives it, because a step that
// passes in isolation can still be unreachable from the step before it.
//
// newUIRequest (admin_ui_session_test.go) fails the test if the fixture address
// is loopback, so every request here traverses the credential chain rather than
// the Path 0 bypass.

// browserGet issues a GET shaped as a browser top-level navigation.
func browserGet(t *testing.T, router http.Handler, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := newUIRequest(t, http.MethodGet, target, "")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", browserNavigationAccept)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// sessionCookie returns the admin_ticket cookie a response set, or nil when it
// set none. The distinction is the assertion in the wrong-credential step: a
// status says nothing about whether a session was handed out.
func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.AdminTicketCookieName {
			return c
		}
	}
	return nil
}

func TestOperatorReachesTheAdminUIInABrowser(t *testing.T) {
	router, _ := newUIRouter(t)
	jsPath, _ := builtAssetPaths(t)

	// Step 1: the operator types the server address. The response has to lead
	// somewhere they can act on, and it must not be the shell.
	first := browserGet(t, router, "/", nil)
	// The withheld body is checked before the status: a leak that carried a 303
	// would otherwise be masked by the status check failing first.
	if strings.Contains(first.Body.String(), spaMountPoint) {
		t.Errorf("step 1 returned the shell to an unauthenticated navigation: body contains %s", spaMountPoint)
	}
	if first.Code != http.StatusSeeOther {
		t.Fatalf("step 1, unauthenticated navigation to /: status = %d, want 303; body = %q", first.Code, first.Body.String())
	}
	if got := first.Header().Get("WWW-Authenticate"); got != "" {
		t.Errorf("step 1 named an authentication scheme (%q); no current browser prompts for one, so the operator would see a dead end", got)
	}
	loginTarget := first.Header().Get("Location")
	if loginTarget == "" {
		t.Fatal("step 1 sent a redirect with no Location; the browser has nowhere to go")
	}

	// Step 2: the browser follows that Location. The target is read from the
	// response rather than written down here, so the test walks the same path
	// the browser does.
	form := browserGet(t, router, loginTarget, nil)
	if form.Code != http.StatusOK {
		t.Fatalf("step 2, GET %s: status = %d, want 200; body = %q", loginTarget, form.Code, form.Body.String())
	}
	if ct := form.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Errorf("step 2, GET %s Content-Type = %q, want HTML the browser renders", loginTarget, ct)
	}
	for _, want := range []string{`action="/auth/login"`, `name="key"`, `type="password"`, "Sign in"} {
		if !strings.Contains(form.Body.String(), want) {
			t.Errorf("step 2, GET %s body is missing %q; the operator has no field to type the key into", loginTarget, want)
		}
	}

	// Step 3: a mistyped key. No session may be issued, and the next
	// navigation must still be refused.
	wrong := newUIRequest(t, http.MethodPost, "/auth/login", url.Values{"key": []string{"not-the-key"}}.Encode())
	wrongRec := httptest.NewRecorder()
	router.ServeHTTP(wrongRec, wrong)
	if c := sessionCookie(wrongRec); c != nil {
		t.Fatalf("step 3, a wrong key issued %s=%q", c.Name, c.Value)
	}
	if !strings.Contains(wrongRec.Body.String(), "Invalid admin key.") {
		t.Errorf("step 3, a wrong key did not re-render the form with an error; body = %q", wrongRec.Body.String())
	}
	stillRefused := browserGet(t, router, "/", nil)
	if stillRefused.Code != http.StatusSeeOther || strings.Contains(stillRefused.Body.String(), spaMountPoint) {
		t.Fatalf("step 3, after a wrong key: status = %d and shell-in-body = %v, want 303 and false",
			stillRefused.Code, strings.Contains(stillRefused.Body.String(), spaMountPoint))
	}

	// Step 4: the right key. The cookie is the session, and its flags are
	// asserted where they are set.
	cookie := loginForCookie(t, router)

	// Step 5: three sequential navigations on that one cookie, then a fourth.
	// The third succeeding is the property: a session that were consumed on
	// validation would admit the first and drop the rest.
	for i, step := range []struct {
		path      string
		wantShell bool
	}{
		{path: "/", wantShell: true},
		{path: "/ui/", wantShell: true},
		{path: jsPath},
		{path: "/", wantShell: true},
	} {
		rec := browserGet(t, router, step.path, cookie)
		if rec.Code != http.StatusOK {
			t.Fatalf("step 5.%d, GET %s on the session cookie: status = %d, want 200; body = %q", i+1, step.path, rec.Code, rec.Body.String())
		}
		if step.wantShell {
			if !strings.Contains(rec.Body.String(), spaMountPoint) {
				t.Errorf("step 5.%d, GET %s returned 200 without the shell: body = %q", i+1, step.path, rec.Body.String())
			}
		} else if got, want := rec.Body.Bytes(), mustReadDist(t, step.path); string(got) != string(want) {
			t.Errorf("step 5.%d, GET %s returned %d bytes, want the %d bytes of the committed asset", i+1, step.path, len(got), len(want))
		}
		if c := sessionCookie(rec); c != nil {
			t.Errorf("step 5.%d, GET %s re-set %s=%q; only the login handler may set it, and a rotation cannot survive one page's parallel subresource loads",
				i+1, step.path, c.Name, c.Value)
		}
	}
}

// TestAdminUIRefusalNeverDeliversTheLoginPageToCode is the other half of the
// split: the login form is HTML, and a consumer that asked for a script or JSON
// parses whatever it gets as that. A login page delivered into a <script> is a
// blank shell with no server-side signal.
func TestAdminUIRefusalNeverDeliversTheLoginPageToCode(t *testing.T) {
	router, _ := newUIRouter(t)
	jsPath, cssPath := builtAssetPaths(t)

	for _, tc := range []struct {
		path string
		dest string
	}{
		{path: jsPath, dest: "script"},
		{path: cssPath, dest: "style"},
		{path: "/ui/favicon.svg", dest: "image"},
		{path: "/dashboard/data", dest: "empty"},
		{path: "/api/topology", dest: "empty"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			req := newUIRequest(t, http.MethodGet, tc.path, "")
			req.Header.Set("Sec-Fetch-Dest", tc.dest)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s as %s with no credential: status = %d, want 401", tc.path, tc.dest, rec.Code)
			}
			for _, forbidden := range []string{"<form", `name="key"`, "<!DOCTYPE html>", spaMountPoint} {
				if strings.Contains(rec.Body.String(), forbidden) {
					t.Errorf("GET %s as %s returned markup containing %q; body = %q", tc.path, tc.dest, forbidden, rec.Body.String())
				}
			}
		})
	}
}
