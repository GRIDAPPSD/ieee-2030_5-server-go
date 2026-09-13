package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// TestAdminBodyTypeRefusalLogsRefusalWithoutCredentialOrBody covers the
// content-type refusal's own log line: before this, the only line on this
// path was the auth chain's loopback-admission line, identical for a refused
// and an accepted write (#416).
func TestAdminBodyTypeRefusalLogsRefusalWithoutCredentialOrBody(t *testing.T) {
	buf := captureSlogForLogin(t)
	router, _ := server.BuildAdminRouter(
		"the-key", newScopeTestCertService(t), newTestStores(), "GCM",
		auth.NewTicketStore(30*time.Second), auth.NewSessionStore(30*time.Minute, 8*time.Hour),
		server.DefaultAdminAllowedHosts(), false,
	)

	const (
		bearer = "marker-bearer-body-8e21"
		body   = "marker-body-body-c40a"
	)
	req := httptest.NewRequest(http.MethodPost, "/api/fsas", strings.NewReader(`{"description":"`+body+`"}`))
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:50000"
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415", rec.Code)
	}
	if !strings.Contains(buf.String(), `"event":"admin_body_type_refused"`) {
		t.Fatalf("no admin_body_type_refused line captured: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"method":"POST"`) {
		t.Errorf("refusal line missing method: %s", buf.String())
	}
	if !strings.Contains(buf.String(), `"path":"/api/fsas"`) {
		t.Errorf("refusal line missing path: %s", buf.String())
	}
	for _, marker := range []string{bearer, body} {
		if strings.Contains(buf.String(), marker) {
			t.Errorf("refusal log contains %q: %s", marker, buf.String())
		}
	}
}
