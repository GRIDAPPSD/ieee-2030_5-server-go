package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// wantAdminWriteRoutes is the admin plane's state-changing surface with every
// optional service wired. A route added or removed changes this list on
// purpose, so the coverage below is re-read rather than silently widened.
var wantAdminWriteRoutes = []string{
	"DELETE /api/devices/{id}/fsa-assignment",
	"DELETE /api/fsas/{id}",
	"DELETE /api/fsas/{id}/programs",
	"POST /api/certs/device",
	"POST /api/certs/info",
	"POST /api/certs/server",
	"POST /api/devices",
	"POST /api/devices/{id}/fsa-assignment",
	"POST /api/fsas",
	"POST /api/fsas/{id}/programs",
	"POST /auth/login",
	"POST /auth/ticket",
}

// loginPattern decodes only form-encoded bodies (ParseForm ignores any other
// type), so it is covered by the cross-origin refusal but not the body table.
const loginPattern = "POST /auth/login"

// wantAdminBodyTypes is what each handler actually decodes, read from the
// handler source rather than from adminBodyTypes (the table under test): a
// wrong entry in that table must fail this test rather than pass because the
// test asked the table what it expected of itself (#416).
var wantAdminBodyTypes = map[string][]string{
	"POST /api/certs/server":                  {"application/json"},
	"POST /api/certs/device":                  {"application/json"},
	"POST /api/certs/info":                    {"application/x-pem-file", "multipart/form-data"},
	"POST /api/devices":                       {"application/json"},
	"POST /api/fsas":                          {"application/json"},
	"POST /api/fsas/{id}/programs":            {"application/json"},
	"POST /api/devices/{id}/fsa-assignment":   {"application/json"},
	"DELETE /api/fsas/{id}":                   nil,
	"DELETE /api/fsas/{id}/programs":          nil,
	"DELETE /api/devices/{id}/fsa-assignment": nil,
	"POST /auth/ticket":                       nil,
}

// TestEveryAdminWriteRouteIsCovered walks the routes BuildAdminRouter reports
// and drives each one from a loopback address, where the auth chain admits
// anything, so a refusal can only come from the checks under test.
func TestEveryAdminWriteRouteIsCovered(t *testing.T) {
	router, patterns := server.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), newTestStores(), "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		server.DefaultAdminAllowedHosts(), false, nil,
	)

	var writes []string
	for _, p := range patterns {
		method, _, ok := strings.Cut(p, " ")
		if !ok {
			t.Fatalf("admin pattern %q names no method, so it accepts writes this walk cannot enumerate", p)
		}
		switch method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			continue
		}
		writes = append(writes, p)
	}
	if !slices.Equal(writes, wantAdminWriteRoutes) {
		t.Fatalf("admin write routes = %q (%d), want %q (%d)", writes, len(writes), wantAdminWriteRoutes, len(wantAdminWriteRoutes))
	}

	for _, p := range writes {
		if _, declared := server.AdminBodyTypes[p]; !declared && p != loginPattern {
			t.Errorf("%s has no entry in the admin body-type table; the middleware refuses every write to it", p)
		}
	}
	for p := range server.AdminBodyTypes {
		if !slices.Contains(writes, p) {
			t.Errorf("body-type table names %s, which is not a registered admin write route", p)
		}
	}
	for _, p := range writes {
		if p == loginPattern {
			continue
		}
		want, ok := wantAdminBodyTypes[p]
		if !ok {
			t.Fatalf("%s has no independent expectation; wantAdminBodyTypes is out of date", p)
		}
		if got := server.AdminBodyTypes[p]; !slices.Equal(got, want) {
			t.Errorf("%s: adminBodyTypes = %v, want %v (from the handler, not the table under test)", p, got, want)
		}
	}

	for _, p := range writes {
		method, path, _ := strings.Cut(p, " ")
		target := strings.ReplaceAll(path, "{id}", "x")
		// goodType and the expected refusals below come from
		// wantAdminBodyTypes, the independent source, not the production
		// table: a wrong table entry must make this subtest fail rather
		// than silently agree with itself (#416).
		types := wantAdminBodyTypes[p]
		goodType := "application/x-www-form-urlencoded"
		if len(types) > 0 {
			goodType = types[0]
		}

		t.Run(p, func(t *testing.T) {
			rec := serveLoopbackWrite(router, method, target, map[string]string{
				"Content-Type":   goodType,
				"Sec-Fetch-Site": "cross-site",
				"Origin":         "http://attacker.example",
			})
			if rec.Code != http.StatusForbidden || rec.Body.String() != crossOriginRefusalBody {
				t.Errorf("cross-site %s: status = %d body = %q, want 403 %q", p, rec.Code, rec.Body.String(), crossOriginRefusalBody)
			}

			rec = serveLoopbackWrite(router, method, target, map[string]string{
				"Content-Type": goodType,
				"Origin":       "http://attacker.example",
			})
			if rec.Code != http.StatusForbidden || rec.Body.String() != crossOriginRefusalBody {
				t.Errorf("foreign Origin without Sec-Fetch-Site %s: status = %d body = %q, want 403 %q", p, rec.Code, rec.Body.String(), crossOriginRefusalBody)
			}

			if p == loginPattern {
				return
			}

			assertUnsupportedMediaType := func(rec *httptest.ResponseRecorder, label string) {
				t.Helper()
				if rec.Code != http.StatusUnsupportedMediaType {
					t.Errorf("%s %s: status = %d body = %q, want 415", label, p, rec.Code, rec.Body.String())
					return
				}
				var got unsupportedMediaTypeResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatalf("%s %s: 415 body not JSON: %v (%q)", label, p, err, rec.Body.String())
				}
				if got.Error != "unsupported content type" {
					t.Errorf("%s %s: error = %q, want %q", label, p, got.Error, "unsupported content type")
				}
				if !slices.Equal(got.Accepted, types) {
					t.Errorf("%s %s: accepted = %v, want %v", label, p, got.Accepted, types)
				}
			}

			rec = serveLoopbackWrite(router, method, target, map[string]string{"Content-Type": "text/plain;charset=UTF-8"})
			if len(types) > 0 {
				assertUnsupportedMediaType(rec, "text/plain")
			} else if rec.Code == http.StatusUnsupportedMediaType || rec.Code == http.StatusForbidden {
				t.Errorf("text/plain %s reads no body but was refused: status = %d body = %q", p, rec.Code, rec.Body.String())
			}

			// A client that sends no Content-Type at all (a plain curl -d, or
			// a tool that never sets one) must be refused the same way as a
			// mislabelled one, not admitted by default (#416).
			rec = serveLoopbackWrite(router, method, target, map[string]string{})
			if len(types) > 0 {
				assertUnsupportedMediaType(rec, "no Content-Type")
			} else if rec.Code == http.StatusUnsupportedMediaType || rec.Code == http.StatusForbidden {
				t.Errorf("no-Content-Type %s reads no body but was refused: status = %d body = %q", p, rec.Code, rec.Body.String())
			}

			for _, ct := range types {
				rec = serveLoopbackWrite(router, method, target, map[string]string{
					"Content-Type":   ct + "; charset=utf-8",
					"Sec-Fetch-Site": "same-origin",
				})
				if rec.Code == http.StatusUnsupportedMediaType || rec.Code == http.StatusForbidden {
					t.Errorf("same-origin %s as %s did not reach its handler: status = %d body = %q", p, ct, rec.Code, rec.Body.String())
				}
			}
		})
	}
}

// serveLoopbackWrite drives router from a loopback address with a valid
// Bearer credential set by default ("the-key", the same value every caller
// here builds its router with). This test is about body-type and
// cross-origin behavior, not about credential admission, so it was already
// credential-blind before #579: the loopback bypass admitted every request
// regardless. #579 and #631 changed that for the certificate routes, which
// now refuse a bypass-only request before ever reaching the body-type check
// this test exercises, so the default credential keeps those three routes
// exercising the same check as everything else here. A caller that
// overrides "Authorization" in headers still wins, since that assignment
// runs after this default.
func serveLoopbackWrite(router http.Handler, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader("{}"))
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Authorization", "Bearer the-key")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// TestSameOriginAdminUIWritesStillSucceed signs in and makes the SPA's own
// writes as a current browser sends them from the UI's origin, through the
// non-loopback credential chain.
func TestSameOriginAdminUIWritesStillSucceed(t *testing.T) {
	router, _ := newUIRouter(t)
	origin := "https://" + testUIHost

	login := newUIRequest(t, http.MethodPost, "/auth/login", url.Values{"key": []string{"the-key"}}.Encode())
	login.Header.Set("Sec-Fetch-Site", "same-origin")
	login.Header.Set("Sec-Fetch-Mode", "navigate")
	login.Header.Set("Origin", origin)
	loginRec := httptest.NewRecorder()
	router.ServeHTTP(loginRec, login)
	cookie := sessionCookie(loginRec)
	if loginRec.Code != http.StatusSeeOther || cookie == nil {
		t.Fatalf("browser-shaped POST /auth/login: status = %d cookie = %v, want 303 with a session", loginRec.Code, cookie)
	}

	spa := func(method, target, contentType, body string, extra map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := newUIRequest(t, method, target, body)
		req.Header.Del("Content-Type")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		req.Header.Set("Origin", origin)
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	if rec := spa(http.MethodPost, "/auth/ticket", "application/json", "{}", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ticket":"`) {
		t.Fatalf("POST /auth/ticket: status = %d body = %q, want 200 with a ticket", rec.Code, rec.Body.String())
	}
	if rec := spa(http.MethodPost, "/api/fsas", "application/json", `{"description":"roof fleet","primacy":1,"mRID":"fsa-ui"}`, nil); rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/fsas: status = %d body = %q, want 201", rec.Code, rec.Body.String())
	}
	if rec := spa(http.MethodGet, "/api/fsas/fsa-ui", "", "", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "fsa-ui") {
		t.Fatalf("GET /api/fsas/fsa-ui after the create: status = %d body = %q, want 200", rec.Code, rec.Body.String())
	}
	// A body the parser rejects still proves the request reached the handler.
	if rec := spa(http.MethodPost, "/api/certs/info", "application/x-pem-file", "not a certificate", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("POST /api/certs/info: status = %d body = %q, want the handler's 400", rec.Code, rec.Body.String())
	}

	// The same session cannot be spent by another origin.
	if rec := spa(http.MethodDelete, "/api/fsas/fsa-ui", "", "", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://attacker.example"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site DELETE on a live session: status = %d body = %q, want 403", rec.Code, rec.Body.String())
	}
	if rec := spa(http.MethodGet, "/api/fsas/fsa-ui", "", "", nil); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/fsas/fsa-ui after the refused delete: status = %d, want 200", rec.Code)
	}

	if rec := spa(http.MethodDelete, "/api/fsas/fsa-ui", "", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/fsas/fsa-ui: status = %d body = %q, want 204", rec.Code, rec.Body.String())
	}
	if rec := spa(http.MethodGet, "/api/fsas/fsa-ui", "", "", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("GET /api/fsas/fsa-ui after the delete: status = %d, want 404", rec.Code)
	}
}
