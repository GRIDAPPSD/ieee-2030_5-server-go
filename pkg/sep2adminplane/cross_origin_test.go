package sep2adminplane_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2adminplane"
)

const trustedOrigin = "http://192.168.177.10:18444"

// planeFromEnv builds a plane the way an embedder does: SEP2_ADMIN_TRUSTED_ORIGINS
// goes through SettingsFromEnv and the parsed value is copied into Config.
func planeFromEnv(t *testing.T, env map[string]string) *sep2adminplane.Plane {
	t.Helper()
	s, err := sep2adminplane.SettingsFromEnv(envOf(env))
	if err != nil {
		t.Fatalf("SettingsFromEnv: %v", err)
	}
	cfg := baseConfig()
	cfg.TrustedOrigins = s.TrustedOrigins
	return newPlane(t, cfg)
}

// crossSitePost sends an authenticated POST a browser marked cross-site.
func crossSitePost(p *sep2adminplane.Plane, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/fsas", strings.NewReader(`{"description":"x"}`))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testKey)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	req.Header.Set("Origin", origin)
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	return rec
}

func TestTrustedOriginsThroughSettingsAndNew(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		origin  string
		refused bool
	}{
		{"unset refuses a cross-site POST", nil, trustedOrigin, true},
		{"a listed origin passes", map[string]string{"SEP2_ADMIN_TRUSTED_ORIGINS": trustedOrigin}, trustedOrigin, false},
		{"an unlisted origin is refused", map[string]string{"SEP2_ADMIN_TRUSTED_ORIGINS": trustedOrigin}, "http://attacker.example", true},
		{"star passes any origin", map[string]string{"SEP2_ADMIN_TRUSTED_ORIGINS": "*"}, "http://attacker.example", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := crossSitePost(planeFromEnv(t, tc.env), tc.origin)
			if tc.refused {
				if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross-origin admin request refused") {
					t.Fatalf("cross-site POST = %d %s, want 403 cross-origin refusal", rec.Code, rec.Body)
				}
				return
			}
			if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
				t.Fatalf("cross-site POST = %d %s, want it past the cross-origin check", rec.Code, rec.Body)
			}
		})
	}
}

func TestSettingsFromEnvRefusesBadTrustedOrigins(t *testing.T) {
	for _, raw := range []string{"*,", "https://*.example.com", "HTTP://Host", "http://host:80", "http://host/path"} {
		_, err := sep2adminplane.SettingsFromEnv(envOf(map[string]string{"SEP2_ADMIN_TRUSTED_ORIGINS": raw}))
		if err == nil {
			t.Errorf("SettingsFromEnv accepted %q", raw)
			continue
		}
		if !strings.Contains(err.Error(), "SEP2_ADMIN_TRUSTED_ORIGINS") {
			t.Errorf("error %q for %q does not name the setting", err, raw)
		}
	}
}

func TestSettingsFromEnvCarriesTrustedOrigins(t *testing.T) {
	s, err := sep2adminplane.SettingsFromEnv(envOf(map[string]string{"SEP2_ADMIN_TRUSTED_ORIGINS": trustedOrigin}))
	if err != nil {
		t.Fatalf("SettingsFromEnv: %v", err)
	}
	if s.TrustedOrigins != trustedOrigin {
		t.Fatalf("TrustedOrigins = %q, want %q", s.TrustedOrigins, trustedOrigin)
	}
}

func TestNewRefusesBadTrustedOriginsNamingTheSetting(t *testing.T) {
	cfg := baseConfig()
	cfg.TrustedOrigins = "*,"
	p, err := sep2adminplane.New(cfg)
	if err == nil || !strings.Contains(err.Error(), "SEP2_ADMIN_TRUSTED_ORIGINS") {
		t.Fatalf("New error = %v, want one naming SEP2_ADMIN_TRUSTED_ORIGINS", err)
	}
	if p != nil {
		t.Fatal("New returned a plane alongside its error")
	}
}

// slog.SetDefault is process-wide, so these tests do not run in parallel.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestStarWarnsOnceAtNew(t *testing.T) {
	buf := captureWarnings(t)
	p := planeFromEnv(t, map[string]string{"SEP2_ADMIN_TRUSTED_ORIGINS": "*"})
	if n := strings.Count(buf.String(), "admin_cross_origin_disabled"); n != 1 {
		t.Fatalf("WARN count at New = %d, want 1; log %q", n, buf)
	}
	// Serving requests must not log it again.
	crossSitePost(p, "http://attacker.example")
	if n := strings.Count(buf.String(), "admin_cross_origin_disabled"); n != 1 {
		t.Fatalf("WARN count after a request = %d, want 1", n)
	}
}

func TestNoWarnWhenUnsetOrListed(t *testing.T) {
	for _, env := range []map[string]string{nil, {"SEP2_ADMIN_TRUSTED_ORIGINS": trustedOrigin}} {
		buf := captureWarnings(t)
		planeFromEnv(t, env)
		if strings.Contains(buf.String(), "admin_cross_origin_disabled") {
			t.Fatalf("env %v logged the disabled WARN: %q", env, buf)
		}
	}
}
