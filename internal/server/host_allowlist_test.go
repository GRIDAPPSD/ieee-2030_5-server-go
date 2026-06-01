package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/discovery"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
)

// IEEE-138: Host-header allowlist (DNS-rebinding defense).
//
// The middleware MUST run before the auth chain, so wrong-Host requests
// never reach Path 0 / A / B / C / D in internal/auth/admin.go. This file
// pins behavior with table-driven cases plus an integration test that the
// admin login flow respects the gate.

func TestHostAllowlistMiddleware(t *testing.T) {
	cases := []struct {
		name       string
		allowed    []string
		host       string
		proto      string // "HTTP/1.1" (default) or "HTTP/1.0"
		wantStatus int
	}{
		{
			name:       "allowed bare host (localhost)",
			allowed:    []string{"localhost", "127.0.0.1"},
			host:       "localhost",
			wantStatus: http.StatusOK,
		},
		{
			name:       "allowed host:port (localhost:8444 against entry localhost)",
			allowed:    []string{"localhost", "127.0.0.1"},
			host:       "localhost:8444",
			wantStatus: http.StatusOK,
		},
		{
			name:       "allowed host:port (127.0.0.1:8444 against entry 127.0.0.1)",
			allowed:    []string{"localhost", "127.0.0.1"},
			host:       "127.0.0.1:8444",
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejected non-allowlisted host (DNS rebinding)",
			allowed:    []string{"localhost", "127.0.0.1"},
			host:       "evil.example.com",
			wantStatus: http.StatusMisdirectedRequest,
		},
		{
			name:       "rejected non-allowlisted host:port (DNS rebinding with port)",
			allowed:    []string{"localhost", "127.0.0.1"},
			host:       "evil.example.com:8444",
			wantStatus: http.StatusMisdirectedRequest,
		},
		{
			name:       "allowed mDNS hostname (IEEE-133)",
			allowed:    []string{discovery.AdminHostname},
			host:       discovery.AdminHostname,
			wantStatus: http.StatusOK,
		},
		{
			name:       "allowed mDNS hostname:port",
			allowed:    []string{discovery.AdminHostname},
			host:       discovery.AdminHostname + ":8444",
			wantStatus: http.StatusOK,
		},
		{
			name:       "allowed CSV-extended host (operator entry)",
			allowed:    []string{"localhost", "foo.example.com"},
			host:       "foo.example.com",
			wantStatus: http.StatusOK,
		},
		{
			name:       "case-insensitive match (uppercase request)",
			allowed:    []string{"localhost"},
			host:       "LOCALHOST:8444",
			wantStatus: http.StatusOK,
		},
		{
			name:       "case-insensitive match (mixed-case allowlist)",
			allowed:    []string{"Foo.Example.COM"},
			host:       "foo.example.com",
			wantStatus: http.StatusOK,
		},
		{
			name:       "allowed IPv6 loopback (::1)",
			allowed:    []string{"::1"},
			host:       "[::1]:8444",
			wantStatus: http.StatusOK,
		},
		{
			name:       "allowed IPv6 loopback bare (::1)",
			allowed:    []string{"::1"},
			host:       "::1",
			wantStatus: http.StatusOK,
		},
		{
			name:       "rejected IPv6 non-loopback",
			allowed:    []string{"::1"},
			host:       "[2001:db8::1]:8444",
			wantStatus: http.StatusMisdirectedRequest,
		},
		{
			name:       "empty Host on HTTP/1.1 → 400",
			allowed:    []string{"localhost"},
			host:       "",
			proto:      "HTTP/1.1",
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty Host on HTTP/1.0 → 421",
			allowed:    []string{"localhost"},
			host:       "",
			proto:      "HTTP/1.0",
			wantStatus: http.StatusMisdirectedRequest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			})
			h := server.HostAllowlistMiddleware(tc.allowed)(next)

			req := httptest.NewRequest(http.MethodGet, "http://example/anything", nil)
			req.Host = tc.host
			if tc.proto == "HTTP/1.0" {
				req.ProtoMajor, req.ProtoMinor = 1, 0
			} else {
				req.ProtoMajor, req.ProtoMinor = 1, 1
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status: got %d, want %d (body=%q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			passed := rec.Code == http.StatusOK
			if passed && !nextCalled {
				t.Errorf("expected next handler called when status is 200")
			}
			if !passed && nextCalled {
				t.Errorf("next handler must NOT be called on a rejection (status=%d)", rec.Code)
			}
		})
	}
}

func TestHostAllowlistMiddleware_DisabledWhenEmpty(t *testing.T) {
	// Length-0 allowlist short-circuits at the call site (NewAdminRouter
	// skips the wrap). But the middleware itself, when constructed with
	// an empty list, MUST reject everything — there is no "open by
	// default" mode for the middleware itself. The router-level skip is
	// the only opt-out.
	h := server.HostAllowlistMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
	req.Host = "localhost"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("empty allowlist must reject everything, got %d", rec.Code)
	}
}

func TestDefaultAdminAllowedHosts(t *testing.T) {
	got := server.DefaultAdminAllowedHosts()
	want := []string{"localhost", "127.0.0.1", "::1", discovery.AdminHostname}

	gotSorted := append([]string(nil), got...)
	wantSorted := append([]string(nil), want...)
	sort.Strings(gotSorted)
	sort.Strings(wantSorted)

	if strings.Join(gotSorted, ",") != strings.Join(wantSorted, ",") {
		t.Errorf("DefaultAdminAllowedHosts: got %v, want (any order) %v", got, want)
	}

	// IEEE-138: the mDNS hostname MUST come from discovery.AdminHostname
	// (not a hand-typed duplicate), so a future rename in the discovery
	// package propagates here automatically. We can't test "they share a
	// constant" directly, but we CAN assert the value matches.
	found := false
	for _, h := range got {
		if h == discovery.AdminHostname {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("default allowlist missing the mDNS hostname %q", discovery.AdminHostname)
	}
}

func TestResolveAdminAllowedHosts(t *testing.T) {
	cases := []struct {
		name   string
		extras []string
		want   []string
	}{
		{
			name:   "no extras returns defaults",
			extras: nil,
			want:   []string{"localhost", "127.0.0.1", "::1", discovery.AdminHostname},
		},
		{
			name:   "extras appended",
			extras: []string{"foo.example.com", "bar.example.com"},
			want:   []string{"localhost", "127.0.0.1", "::1", discovery.AdminHostname, "foo.example.com", "bar.example.com"},
		},
		{
			name:   "duplicate of default deduped (case-insensitive)",
			extras: []string{"LOCALHOST", "foo.example.com"},
			want:   []string{"localhost", "127.0.0.1", "::1", discovery.AdminHostname, "foo.example.com"},
		},
		{
			name:   "empty/whitespace entries dropped",
			extras: []string{"", "  ", "foo.example.com"},
			want:   []string{"localhost", "127.0.0.1", "::1", discovery.AdminHostname, "foo.example.com"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := server.ResolveAdminAllowedHosts(tc.extras)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("ResolveAdminAllowedHosts(%v):\n got  %v\n want %v", tc.extras, got, tc.want)
			}
		})
	}
}

// Integration: the admin login flow must respect the host gate. A wrong
// Host header on /login must return 421 (defense-in-depth — DNS rebinding
// can target the login page just as easily as the dashboard).
func TestAdminRouterHostAllowlistGatesLogin(t *testing.T) {
	stores := newTestStores()
	tickets := auth.NewTicketStore(5 * time.Minute)
	allowed := []string{"localhost", "127.0.0.1"}
	r := server.NewAdminRouter("the-key", nil, stores, "GCM", tickets, allowed)

	srv := httptest.NewServer(r)
	defer srv.Close()

	// httptest binds 127.0.0.1, so the default Host is 127.0.0.1:<port>
	// and matches the allowlist.
	t.Run("allowlisted host: GET /login is 200", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/login")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("got %d, want 200", resp.StatusCode)
		}
	})

	t.Run("rebound host: GET /login is 421", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/login", nil)
		req.Host = "evil.example.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("got %d, want 421", resp.StatusCode)
		}
	})

	t.Run("rebound host: POST /auth/login is 421", func(t *testing.T) {
		form := url.Values{"key": []string{"the-key"}}
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/auth/login", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Host = "evil.example.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("got %d, want 421 (DNS-rebinding gate must apply to /auth/login too)", resp.StatusCode)
		}
	})

	t.Run("rebound host: GET /api/certs/info is 421 (gate runs before auth)", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/certs/info", nil)
		req.Host = "evil.example.com"
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		// Without the host gate this would be 401 (auth chain rejects).
		// With the gate it's 421 — proves the gate runs OUTSIDE auth.
		if resp.StatusCode != http.StatusMisdirectedRequest {
			t.Errorf("got %d, want 421 (host gate must run before auth chain)", resp.StatusCode)
		}
	})
}
