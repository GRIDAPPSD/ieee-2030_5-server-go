package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The handler sets its own response headers, so an embedder that mounts it
// outside the admin router still gets them.
func TestAdminDeviceLogHeadersAndFailClosedWithoutARouter(t *testing.T) {
	const lfdi = "0123456789ABCDEF0123456789ABCDEF01234567"
	for name, h := range map[string]http.HandlerFunc{
		"read":     (&AdminDeviceLogHandler{}).HandleRead(),
		"download": (&AdminDeviceLogHandler{}).HandleDownload(),
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/device-logs/"+lfdi, nil)
		req.SetPathValue("lfdi", lfdi)
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "device log disabled") {
			t.Errorf("%s with no reader = %d %s, want 503 device log disabled", name, rec.Code, rec.Body)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s headers = %v, want nosniff and no-store", name, rec.Header())
		}
	}
}

func TestParseDeviceLogQueryBounds(t *testing.T) {
	for _, c := range []struct {
		query   string
		limit   int
		wantMsg bool
	}{
		{"", 200, false},
		{"limit=1", 1, false},
		{"limit=1000", 1000, false},
		{"limit=0", 0, true},
		{"limit=1001", 0, true},
		{"limit=%2B5", 5, false},
		{"limit=1e3", 0, true},
		{"scope=managed", 200, false},
		{"scope=Own", 0, true},
		{"kind=request,logevent", 200, false},
		{"kind=request,", 0, true},
	} {
		q, msg := parseDeviceLogQuery(httptest.NewRequest(http.MethodGet, "/x?"+c.query, nil))
		if (msg != "") != c.wantMsg || (!c.wantMsg && q.Limit != c.limit) {
			t.Errorf("query %q: limit=%d msg=%q, want limit=%d refused=%v", c.query, q.Limit, msg, c.limit, c.wantMsg)
		}
	}
}
