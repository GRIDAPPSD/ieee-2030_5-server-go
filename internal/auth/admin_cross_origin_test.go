package auth_test

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

const crossOriginRefusalBody = `{"error":"cross-origin admin request refused"}`

// crossOriginResponse drives the middleware with a request from a loopback
// address, where the auth chain would admit anything, and reports whether the
// wrapped handler ran.
func crossOriginResponse(t *testing.T, req *http.Request) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	reached := false
	h := auth.AdminCrossOriginMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, reached
}

func loopbackAdminRequest(method string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(method, "/api/fsas", strings.NewReader(`{"mRID":"x"}`))
	req.Host = "127.0.0.1:8444"
	req.RemoteAddr = "127.0.0.1:50000"
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func TestAdminCrossOriginMiddleware(t *testing.T) {
	for _, tc := range []struct {
		name    string
		method  string
		headers map[string]string
		refused bool
	}{
		{name: "cross-site POST", method: http.MethodPost, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, refused: true},
		{name: "same-site POST from another localhost port", method: http.MethodPost, headers: map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://localhost:3000"}, refused: true},
		{name: "cross-site DELETE", method: http.MethodDelete, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, refused: true},
		{name: "cross-site PUT", method: http.MethodPut, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, refused: true},
		{name: "cross-site PATCH", method: http.MethodPatch, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, refused: true},
		{name: "same-origin POST from the admin UI", method: http.MethodPost, headers: map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:8444"}, refused: false},
		{name: "user-initiated request", method: http.MethodPost, headers: map[string]string{"Sec-Fetch-Site": "none"}, refused: false},

		{name: "no Sec-Fetch-Site, no Origin: curl-style tool", method: http.MethodPost, headers: nil, refused: false},
		{name: "no Sec-Fetch-Site, no Origin: curl-style DELETE", method: http.MethodDelete, headers: nil, refused: false},
		{name: "no Sec-Fetch-Site, Origin matches Host: older browser on the admin UI", method: http.MethodPost, headers: map[string]string{"Origin": "http://127.0.0.1:8444"}, refused: false},
		{name: "no Sec-Fetch-Site, foreign Origin: older browser on another site", method: http.MethodPost, headers: map[string]string{"Origin": "http://attacker.example"}, refused: true},
		{name: "no Sec-Fetch-Site, Origin on another port of the same host", method: http.MethodPost, headers: map[string]string{"Origin": "http://127.0.0.1:3000"}, refused: true},
		{name: "no Sec-Fetch-Site, opaque Origin", method: http.MethodPost, headers: map[string]string{"Origin": "null"}, refused: true},

		{name: "cross-site GET", method: http.MethodGet, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, refused: false},
		{name: "cross-site HEAD", method: http.MethodHead, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, refused: false},
		{name: "cross-site OPTIONS", method: http.MethodOptions, headers: map[string]string{"Sec-Fetch-Site": "cross-site"}, refused: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, reached := crossOriginResponse(t, loopbackAdminRequest(tc.method, tc.headers))

			if !tc.refused {
				if !reached || rec.Code != http.StatusNoContent {
					t.Fatalf("%s with %v: reached = %v, status = %d; want the handler to run", tc.method, tc.headers, reached, rec.Code)
				}
				return
			}
			if reached {
				t.Fatalf("%s with %v reached the handler", tc.method, tc.headers)
			}
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s with %v: status = %d, want 403", tc.method, tc.headers, rec.Code)
			}
			if got := rec.Body.String(); got != crossOriginRefusalBody {
				t.Errorf("%s with %v: body = %q, want %q", tc.method, tc.headers, got, crossOriginRefusalBody)
			}
			if got := rec.Header().Get("Content-Type"); got != "application/json" {
				t.Errorf("%s with %v: Content-Type = %q, want application/json", tc.method, tc.headers, got)
			}
			for _, name := range []string{"Location", "WWW-Authenticate", "Vary"} {
				if got := rec.Header().Get(name); got != "" {
					t.Errorf("%s with %v: %s = %q, want none", tc.method, tc.headers, name, got)
				}
			}
		})
	}
}

// TestAdminCrossOriginRefusalLogsNoCredentialOrBody sends every credential
// kind and a body carrying distinct markers, and requires the refusal line to
// exist while none of the markers appear in either log sink.
func TestAdminCrossOriginRefusalLogsNoCredentialOrBody(t *testing.T) {
	slogBuf := captureSlog(t)
	var stdBuf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(&stdBuf)
	t.Cleanup(func() { log.SetOutput(prevOut) })

	const (
		bearer = "marker-bearer-4f1c"
		cookie = "marker-cookie-9a2e"
		ticket = "marker-ticket-31b8"
		body   = "marker-body-77d0"
	)
	req := httptest.NewRequest(http.MethodPost, "/api/devices?ticket="+ticket, strings.NewReader(`{"pin":"`+body+`"}`))
	req.Host = "127.0.0.1:8444"
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.AddCookie(&http.Cookie{Name: auth.AdminTicketCookieName, Value: cookie})

	rec, reached := crossOriginResponse(t, req)
	if reached || rec.Code != http.StatusForbidden {
		t.Fatalf("reached = %v, status = %d; want a 403 refusal", reached, rec.Code)
	}

	found := false
	for _, line := range logLines(t, slogBuf) {
		if line["event"] == "admin_cross_origin_refused" && line["method"] == http.MethodPost && line["path"] == "/api/devices" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no admin_cross_origin_refused line was captured, so the leak assertions below would pass on an empty log: %s", slogBuf.String())
	}
	for _, marker := range []string{bearer, cookie, ticket, body} {
		if strings.Contains(slogBuf.String(), marker) || strings.Contains(stdBuf.String(), marker) {
			t.Errorf("refusal logged %q:\nslog: %s\nlog: %s", marker, slogBuf.String(), stdBuf.String())
		}
	}
}

// TestAdminCrossOriginRefusalLogsErrorAndOrigin covers the refusal line's
// diagnostic fields: without them, an operator sees that a request was
// refused but not what check failed or where it claimed to come from (#416).
func TestAdminCrossOriginRefusalLogsErrorAndOrigin(t *testing.T) {
	buf := captureSlog(t)
	req := loopbackAdminRequest(http.MethodPost, map[string]string{
		"Sec-Fetch-Site": "cross-site",
		"Origin":         "http://attacker.example",
	})

	rec, reached := crossOriginResponse(t, req)
	if reached || rec.Code != http.StatusForbidden {
		t.Fatalf("reached = %v, status = %d; want a 403 refusal", reached, rec.Code)
	}

	found := false
	for _, line := range logLines(t, buf) {
		if line["event"] != "admin_cross_origin_refused" {
			continue
		}
		found = true
		if line["origin"] != "http://attacker.example" {
			t.Errorf("origin = %v, want the refused request's Origin header", line["origin"])
		}
		if errVal, _ := line["err"].(string); errVal == "" {
			t.Errorf("err missing or empty on the refusal line: %v", line)
		}
	}
	if !found {
		t.Fatalf("no admin_cross_origin_refused line was captured: %s", buf.String())
	}
}
