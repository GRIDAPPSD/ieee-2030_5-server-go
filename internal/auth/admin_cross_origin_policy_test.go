package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

func policyResponse(t *testing.T, p *auth.CrossOriginPolicy, headers map[string]string) bool {
	t.Helper()
	_, reached := func() (any, bool) {
		reached := false
		h := p.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusNoContent)
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, loopbackAdminRequest(http.MethodPost, headers))
		return nil, reached
	}()
	return reached
}

func TestTrustedOriginsPolicy(t *testing.T) {
	const trusted = "http://192.168.177.10:18444"
	cross := func(origin string) map[string]string {
		return map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": origin}
	}
	for _, tc := range []struct {
		name    string
		raw     string
		headers map[string]string
		admits  bool
	}{
		{"unset refuses a cross-origin POST", "", cross(trusted), false},
		{"blank value is unset", " , ", cross(trusted), false},
		{"trusted origin passes", trusted, cross(trusted), true},
		{"trusted origin among several passes", "https://a.example, " + trusted, cross(trusted), true},
		{"untrusted origin is refused", trusted, cross("http://attacker.example"), false},
		{"trusted origin on another port is refused", trusted, cross("http://192.168.177.10:9"), false},
		{"star admits any cross-origin POST", "*", cross("http://attacker.example"), true},
		{"star admits cross-site with no Origin", "*", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := auth.ParseTrustedOrigins(tc.raw)
			if err != nil {
				t.Fatalf("ParseTrustedOrigins(%q): %v", tc.raw, err)
			}
			if got := policyResponse(t, p, tc.headers); got != tc.admits {
				t.Errorf("admitted = %v, want %v", got, tc.admits)
			}
			if got, want := p.Disabled(), tc.raw == "*"; got != want {
				t.Errorf("Disabled() = %v, want %v", got, want)
			}
		})
	}
}

func TestTrustedOriginsRefusesStartup(t *testing.T) {
	for _, tc := range []struct {
		name, raw, mention string
	}{
		{"no scheme", "192.168.177.10:18444", "192.168.177.10:18444"},
		{"path in origin", "http://host.example/admin", "http://host.example/admin"},
		{"bad entry after a good one", "http://ok.example,not an origin", "not an origin"},
		{"star beside an origin", "*,http://ok.example", "*"},
		{"origin beside star", "http://ok.example, *", "*"},
		{"star with trailing comma", "*,", "*"},
		{"star with leading comma", ",*", "*"},
		{"wildcard host", "https://*.example.com", "https://*.example.com"},
		{"uppercase scheme", "HTTP://host.example", "HTTP://host.example"},
		{"uppercase host", "http://Host.Example", "http://Host.Example"},
		{"explicit https default port", "https://a.example:443", "https://a.example:443"},
		{"explicit http default port", "http://a.example:80", "http://a.example:80"},
		{"userinfo", "http://u@a.example", "http://u@a.example"},
		{"query", "http://a.example?x=1", "http://a.example?x=1"},
		{"fragment", "http://a.example#f", "http://a.example#f"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := auth.ParseTrustedOrigins(tc.raw)
			if err == nil {
				t.Fatalf("ParseTrustedOrigins(%q) = %+v, nil; want an error", tc.raw, p)
			}
			if !strings.Contains(err.Error(), tc.mention) || !strings.Contains(err.Error(), "SEP2_ADMIN_TRUSTED_ORIGINS") {
				t.Errorf("error %q does not name the setting and %q", err, tc.mention)
			}
		})
	}
}

func TestStarWithSurroundingWhitespaceDisablesTheCheck(t *testing.T) {
	p, err := auth.ParseTrustedOrigins(" \t*\n ")
	if err != nil {
		t.Fatalf("ParseTrustedOrigins: %v", err)
	}
	if !p.Disabled() {
		t.Error("whitespace around a lone * should still disable the check")
	}
}

func TestZeroValuePolicyIsTheDefault(t *testing.T) {
	var zero auth.CrossOriginPolicy
	headers := map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://attacker.example"}
	if policyResponse(t, &zero, headers) {
		t.Error("zero-value policy admitted a cross-site POST")
	}
	if zero.Disabled() {
		t.Error("zero-value policy reports the check off")
	}
	if !policyResponse(t, &zero, map[string]string{"Sec-Fetch-Site": "same-origin"}) {
		t.Error("zero-value policy refused a same-origin POST")
	}
}
