package server_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

const (
	crossOriginRefusalBody   = `{"error":"cross-origin admin request refused"}`
	unsupportedMediaTypeBody = `{"error":"unsupported content type"}`
)

// newLoopbackAdminServer serves the production admin router over a real
// loopback socket, which is the position a browser on the host is in: the
// Path 0 bypass admits every request, so only a provenance or content-type
// check stands between a page and the handlers.
func newLoopbackAdminServer(t *testing.T) *httptest.Server {
	t.Helper()
	router, _ := server.BuildAdminRouter(
		"the-key", nil, newTestStores(), "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		server.DefaultAdminAllowedHosts(), false,
	)
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	host, _, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("test server bound %s, not loopback: the bypass would not admit and a refusal would prove nothing", host)
	}
	return srv
}

func doAdmin(t *testing.T, method, url, body string, headers map[string]string) (int, http.Header, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(b)
}

// TestCrossOriginFSACreateIsRefusedOverSocket drives the demonstrated write
// from a loopback client, then reads the resource back to show nothing landed.
func TestCrossOriginFSACreateIsRefusedOverSocket(t *testing.T) {
	srv := newLoopbackAdminServer(t)

	for _, tc := range []struct {
		name       string
		mrid       string
		headers    map[string]string
		wantStatus int
		wantBody   string
	}{
		{
			name: "current browser, cross-site page",
			mrid: "fsa-xsite",
			headers: map[string]string{
				"Content-Type":   "text/plain;charset=UTF-8",
				"Sec-Fetch-Site": "cross-site",
				"Sec-Fetch-Mode": "no-cors",
				"Origin":         "http://attacker.example",
			},
			wantStatus: http.StatusForbidden,
			wantBody:   crossOriginRefusalBody,
		},
		{
			name: "current browser, another port on localhost",
			mrid: "fsa-samesite",
			headers: map[string]string{
				"Content-Type":   "text/plain;charset=UTF-8",
				"Sec-Fetch-Site": "same-site",
				"Origin":         "http://localhost:3000",
			},
			wantStatus: http.StatusForbidden,
			wantBody:   crossOriginRefusalBody,
		},
		{
			name: "older browser, Origin only",
			mrid: "fsa-origin-only",
			headers: map[string]string{
				"Content-Type": "text/plain;charset=UTF-8",
				"Origin":       "http://attacker.example",
			},
			wantStatus: http.StatusForbidden,
			wantBody:   crossOriginRefusalBody,
		},
		{
			name: "opaque origin from a sandboxed frame",
			mrid: "fsa-null-origin",
			headers: map[string]string{
				"Content-Type": "text/plain;charset=UTF-8",
				"Origin":       "null",
			},
			wantStatus: http.StatusForbidden,
			wantBody:   crossOriginRefusalBody,
		},
		{
			name:       "no provenance headers, mislabelled body",
			mrid:       "fsa-no-headers",
			headers:    map[string]string{"Content-Type": "text/plain;charset=UTF-8"},
			wantStatus: http.StatusUnsupportedMediaType,
			wantBody:   unsupportedMediaTypeBody,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"description":"planted","primacy":1,"mRID":"` + tc.mrid + `"}`
			status, hdr, got := doAdmin(t, http.MethodPost, srv.URL+"/api/fsas", body, tc.headers)
			if status != tc.wantStatus {
				t.Errorf("POST /api/fsas: status = %d, want %d; body = %q", status, tc.wantStatus, got)
			}
			if got != tc.wantBody {
				t.Errorf("POST /api/fsas: body = %q, want %q", got, tc.wantBody)
			}
			if ct := hdr.Get("Content-Type"); ct != "application/json" {
				t.Errorf("POST /api/fsas refusal Content-Type = %q, want application/json", ct)
			}
			if cc := hdr.Get("Cache-Control"); cc != "no-store" {
				t.Errorf("POST /api/fsas refusal Cache-Control = %q, want the admin plane's no-store", cc)
			}

			readStatus, _, readBody := doAdmin(t, http.MethodGet, srv.URL+"/api/fsas/"+tc.mrid, "", nil)
			if readStatus != http.StatusNotFound {
				t.Fatalf("GET /api/fsas/%s after the refused write: status = %d, want 404; the write landed: %q", tc.mrid, readStatus, readBody)
			}
		})
	}

	// Requests a browser cannot be shown to have sent cross-origin still land,
	// and landing is what makes the 404 read-backs above mean "not created"
	// rather than "read path broken".
	for _, tc := range []struct {
		name    string
		mrid    string
		headers map[string]string
	}{
		{name: "current browser on the admin UI", mrid: "fsa-same-origin", headers: map[string]string{
			"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin", "Origin": srv.URL,
		}},
		{name: "older browser on the admin UI, Origin matches Host", mrid: "fsa-old-browser", headers: map[string]string{
			"Content-Type": "application/json", "Origin": srv.URL,
		}},
		{name: "curl-style tool, no provenance headers", mrid: "fsa-curl", headers: map[string]string{
			"Content-Type": "application/json",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"description":"operator","primacy":1,"mRID":"` + tc.mrid + `"}`
			if status, _, got := doAdmin(t, http.MethodPost, srv.URL+"/api/fsas", body, tc.headers); status != http.StatusCreated {
				t.Fatalf("POST /api/fsas: status = %d, want 201; body = %q", status, got)
			}
			if status, _, got := doAdmin(t, http.MethodGet, srv.URL+"/api/fsas/"+tc.mrid, "", nil); status != http.StatusOK || !strings.Contains(got, tc.mrid) {
				t.Fatalf("GET /api/fsas/%s after the create: status = %d body = %q, want 200 naming the FSA", tc.mrid, status, got)
			}
		})
	}
}
