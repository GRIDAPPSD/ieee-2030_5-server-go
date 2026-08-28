package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// #413: neither of this defect's two visible symptoms is observable through
// the deployed router. Cache-Control is masked by the plane-wide outer
// wrapper, which sets it before this handler ever runs. The sniffed
// Content-Type coincides byte-for-byte with the explicit one, because
// loginHTML starts with "<!DOCTYPE html>" and that is exactly what
// net/http's content sniffer returns for it. A value assertion on either
// header would therefore pass today, for the wrong reason.
//
// The mechanism is call ORDER: net/http snapshots the header map the moment
// WriteHeader first runs, so any Header().Set after that point is silently
// dropped. This file drives every branch of HandleLoginPage and
// HandleLoginSubmit through a shim that records call order, and asserts that
// no WriteHeader precedes that branch's own header Set calls.

// recordingResponseWriter wraps an http.ResponseWriter and records the order
// Header, WriteHeader and Write are called in.
type recordingResponseWriter struct {
	http.ResponseWriter
	calls []string
}

func (rw *recordingResponseWriter) Header() http.Header {
	rw.calls = append(rw.calls, "Header")
	return rw.ResponseWriter.Header()
}

func (rw *recordingResponseWriter) WriteHeader(code int) {
	rw.calls = append(rw.calls, "WriteHeader")
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *recordingResponseWriter) Write(b []byte) (int, error) {
	rw.calls = append(rw.calls, "Write")
	return rw.ResponseWriter.Write(b)
}

func firstIndex(calls []string, name string) int {
	for i, c := range calls {
		if c == name {
			return i
		}
	}
	return -1
}

// TestLoginHandlersNeverWriteHeaderBeforeSettingHeaders covers every branch
// of HandleLoginPage and HandleLoginSubmit: the ordering mistake is checked
// for anywhere in the file, not just the one call site the fix removes.
func TestLoginHandlersNeverWriteHeaderBeforeSettingHeaders(t *testing.T) {
	sessions := auth.NewSessionStore(30*time.Minute, 8*time.Hour)
	formReq := func(key string) *http.Request {
		form := url.Values{"key": []string{key}}
		r := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}

	cases := []struct {
		name string
		req  *http.Request
		h    http.HandlerFunc
	}{
		{
			name: "GET /login normal render",
			req:  httptest.NewRequest(http.MethodGet, "/login", nil),
			h:    server.HandleLoginPage(""),
		},
		{
			name: "GET /login with an error message",
			req:  httptest.NewRequest(http.MethodGet, "/login", nil),
			h:    server.HandleLoginPage("Invalid admin key."),
		},
		{
			name: "POST /auth/login wrong key",
			req:  formReq("wrong"),
			h:    server.HandleLoginSubmit("the-secret", sessions),
		},
		{
			name: "POST /auth/login blank key",
			req:  formReq("   "),
			h:    server.HandleLoginSubmit("the-secret", sessions),
		},
		{
			name: "POST /auth/login correct key",
			req:  formReq("the-secret"),
			h:    server.HandleLoginSubmit("the-secret", sessions),
		},
		{
			name: "POST /auth/login form parse error",
			req: func() *http.Request {
				r := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader("key=%ZZ"))
				r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				return r
			}(),
			h: server.HandleLoginSubmit("the-secret", sessions),
		},
		{
			name: "POST /auth/login no admin key configured",
			req:  formReq("anything"),
			h:    server.HandleLoginSubmit("", sessions),
		},
		{
			name: "GET /auth/login method not allowed",
			req:  httptest.NewRequest(http.MethodGet, "/auth/login", nil),
			h:    server.HandleLoginSubmit("the-secret", sessions),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			rw := &recordingResponseWriter{ResponseWriter: rec}
			tc.h(rw, tc.req)

			wh := firstIndex(rw.calls, "WriteHeader")
			hs := firstIndex(rw.calls, "Header")
			if hs == -1 {
				t.Fatalf("handler never called Header() to set a header; call sequence = %v", rw.calls)
			}
			if wh != -1 && wh < hs {
				t.Errorf("WriteHeader (call %d) precedes the first header Set (call %d); sequence = %v; any header set at or after this point is silently dropped",
					wh, hs, rw.calls)
			}
		})
	}
}
