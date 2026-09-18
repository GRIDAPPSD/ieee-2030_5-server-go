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

// #159: ensure the admin router exposes /login + /auth/login outside the
// auth middleware and that the cookie issued by /auth/login unlocks an
// authenticated GET / on the next request.

func TestAdminRouterLoginPagePublic(t *testing.T) {
	stores := newTestStores()
	tickets := auth.NewTicketStore(5 * time.Minute)
	sessions := auth.NewSessionStore(30*time.Minute, 8*time.Hour)
	r, _ := server.BuildAdminRouter("the-key", nil, stores, "GCM", tickets, sessions, nil, false)

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Unauthenticated GET /login must succeed.
	resp, err := http.Get(srv.URL + "/login")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/login should be public, got %d", resp.StatusCode)
	}
}

func TestAdminRouterLoginCookieGrantsAccess(t *testing.T) {
	stores := newTestStores()
	tickets := auth.NewTicketStore(5 * time.Minute)
	sessions := auth.NewSessionStore(30*time.Minute, 8*time.Hour)
	r, _ := server.BuildAdminRouter("the-key", nil, stores, "GCM", tickets, sessions, nil, false)

	srv := httptest.NewServer(r)
	defer srv.Close()

	// 1) POST /auth/login with correct key.
	form := url.Values{"key": []string{"the-key"}}
	jar := newCookieJar()
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // stop at the 303
		},
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /auth/login expected 303, got %d", resp.StatusCode)
	}

	// Cookie should now be in the jar.
	var ok bool
	for _, c := range jar.cookies {
		if c.Name == auth.AdminTicketCookieName && c.Value != "" {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("admin_ticket cookie not stored in jar after login: %+v", jar.cookies)
	}

	// 2) Subsequent GET / should be authenticated via the cookie.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("authenticated GET / expected 200, got %d", resp2.StatusCode)
	}
}

func TestAdminRouterApiCertsInfoBehindAuth(t *testing.T) {
	stores := newTestStores()
	tickets := auth.NewTicketStore(5 * time.Minute)
	sessions := auth.NewSessionStore(30*time.Minute, 8*time.Hour)
	r, _ := server.BuildAdminRouter("the-key", nil, stores, "GCM", tickets, sessions, nil, false)

	srv := httptest.NewServer(r)
	defer srv.Close()

	// Unauthenticated must 401. #246: the test server binds 127.0.0.1
	// which would trigger the loopback bypass; XFF simulates the
	// production-fronted-by-Caddy case so the bypass declines.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/certs/info", strings.NewReader("not-a-cert"))
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/certs/info unauthenticated expected 401, got %d", resp.StatusCode)
	}
}

// TestAdminRouterApiCertsDeviceTypesReachableWithoutCertService proves
// GET /api/certs/device-types is registered outside the `svc != nil` guard
// (#594): the device type vocabulary is fixed, not tied to whether a CA is
// configured on this server instance. A 401 (not 404) confirms the route
// exists and requires the same auth as its neighbours.
func TestAdminRouterApiCertsDeviceTypesReachableWithoutCertService(t *testing.T) {
	stores := newTestStores()
	tickets := auth.NewTicketStore(5 * time.Minute)
	sessions := auth.NewSessionStore(30*time.Minute, 8*time.Hour)
	r, _ := server.BuildAdminRouter("the-key", nil, stores, "GCM", tickets, sessions, nil, false)

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/certs/device-types", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/api/certs/device-types unauthenticated expected 401, got %d", resp.StatusCode)
	}
}

// minimal cookie jar; httptest.Server is HTTP not HTTPS, so the Secure flag
// would block the real net/http/cookiejar. We capture cookies eagerly and
// resend them on every request.

type recordingJar struct {
	cookies []*http.Cookie
}

func newCookieJar() *recordingJar { return &recordingJar{} }

func (j *recordingJar) SetCookies(_ *url.URL, cs []*http.Cookie) {
	j.cookies = append(j.cookies, cs...)
}

func (j *recordingJar) Cookies(_ *url.URL) []*http.Cookie {
	return j.cookies
}
