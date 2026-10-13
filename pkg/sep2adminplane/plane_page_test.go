package sep2adminplane_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2adminplane"
)

// pageHit records what one request to the page handler saw.
type pageHit struct {
	method, path, body string
}

// pageConfig is baseConfig with one page panel "tool", whose handler records
// each request and answers through respond, and one descriptor panel "table".
func pageConfig(hits *[]pageHit, respond func(http.ResponseWriter)) sep2adminplane.Config {
	cfg := baseConfig()
	cfg.Panels = []sep2admin.Panel{
		{
			ID:                "tool",
			Label:             "Tool",
			Placement:         sep2admin.ExtensionSlot(1),
			DescriptorVersion: sep2admin.CurrentDescriptorVersion,
			Page: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				b, _ := io.ReadAll(r.Body)
				*hits = append(*hits, pageHit{r.Method, r.URL.Path, string(b)})
				respond(w)
			}),
		},
		{
			ID:                "table",
			Label:             "Table",
			Placement:         sep2admin.ExtensionSlot(2),
			DescriptorVersion: sep2admin.CurrentDescriptorVersion,
			View: func(context.Context) (sep2admin.Descriptor, error) {
				return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion}, nil
			},
		},
	}
	return cfg
}

func writeOK(w http.ResponseWriter) { _, _ = io.WriteString(w, "page body") }

// login posts the admin key to /auth/login and returns the session cookie.
func login(t *testing.T, p *sep2adminplane.Plane) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(url.Values{"key": {testKey}}.Encode()))
	req.RemoteAddr = "127.0.0.1:40000"
	req.Host = "localhost"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("login = %d with %d cookies, want 303 and one session cookie", rec.Code, len(rec.Result().Cookies()))
	}
	return rec.Result().Cookies()[0]
}

// pageRequest builds a request to the plane from a non-loopback address, so
// no bypass can admit it.
func pageRequest(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:40000"
	req.Host = "localhost"
	return req
}

func serve(p *sep2adminplane.Plane, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	return rec
}

func TestPagePanelIsBehindThePlaneChecks(t *testing.T) {
	var hits []pageHit
	cfg := pageConfig(&hits, writeOK)
	cfg.ReadOnly = true
	p := newPlane(t, cfg)
	session := login(t, p)

	t.Run("401 without a credential", func(t *testing.T) {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
			if rec := serve(p, pageRequest(m, "/apps/tool/", "")); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s /apps/tool/ with no credential = %d, want 401", m, rec.Code)
			}
		}
	})

	t.Run("421 on a wrong Host", func(t *testing.T) {
		req := pageRequest(http.MethodGet, "/apps/tool/", "")
		req.Host = "attacker.example"
		req.AddCookie(session)
		if rec := serve(p, req); rec.Code != http.StatusMisdirectedRequest {
			t.Errorf("GET with a wrong Host = %d, want 421", rec.Code)
		}
	})

	t.Run("403 on a cross-site POST", func(t *testing.T) {
		req := pageRequest(http.MethodPost, "/apps/tool/save", "x")
		req.AddCookie(session)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Origin", "http://attacker.example")
		if rec := serve(p, req); rec.Code != http.StatusForbidden {
			t.Errorf("cross-site POST = %d, want 403", rec.Code)
		}
	})

	if len(hits) != 0 {
		t.Fatalf("the page handler ran for a refused request: %+v", hits)
	}

	t.Run("200 with a session, prefix stripped", func(t *testing.T) {
		req := pageRequest(http.MethodGet, "/apps/tool/", "")
		req.AddCookie(session)
		rec := serve(p, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "page body" {
			t.Fatalf("GET with a session = %d %q, want 200 page body", rec.Code, rec.Body)
		}
		if got := hits[len(hits)-1]; got.method != http.MethodGet || got.path != "/" {
			t.Fatalf("handler saw %+v, want GET /", got)
		}
	})

	t.Run("same-origin POST reaches the handler with ReadOnly on", func(t *testing.T) {
		req := pageRequest(http.MethodPost, "/apps/tool/save", "a=1")
		req.AddCookie(session)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := serve(p, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("same-origin POST = %d %s, want 200", rec.Code, rec.Body)
		}
		if got := hits[len(hits)-1]; got != (pageHit{http.MethodPost, "/save", "a=1"}) {
			t.Fatalf("handler saw %+v, want POST /save a=1", got)
		}
	})

	t.Run("an id that is not a page panel is 404", func(t *testing.T) {
		before := len(hits)
		for _, path := range []string{"/apps/table/", "/apps/none/"} {
			req := pageRequest(http.MethodGet, path, "")
			req.AddCookie(session)
			if rec := serve(p, req); rec.Code != http.StatusNotFound {
				t.Errorf("GET %s = %d, want 404", path, rec.Code)
			}
		}
		if len(hits) != before {
			t.Fatalf("the page handler ran for another id")
		}
	})
}

// The loopback bypass admits a credential-free request to most reads; a page
// may disclose anything, so it never does.
func TestLoopbackBypassDoesNotReachAPage(t *testing.T) {
	var hits []pageHit
	cfg := pageConfig(&hits, writeOK)
	cfg.LoopbackBypass = true
	p := newPlane(t, cfg)
	for _, m := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodOptions} {
		if rec := send(p, m, "/apps/tool/x", "", false); rec.Code != http.StatusUnauthorized {
			t.Errorf("bypass on: loopback %s with no credential = %d, want 401", m, rec.Code)
		}
	}
	if len(hits) != 0 {
		t.Fatalf("the page handler ran with no credential: %+v", hits)
	}
	// The control: the bypass does admit a credential-free read elsewhere.
	if rec := send(p, http.MethodGet, "/ui/", "", false); rec.Code != http.StatusOK {
		t.Fatalf("bypass on: GET /ui/ = %d, want 200", rec.Code)
	}
}

func cspFrameAncestors(h http.Header) []string {
	var out []string
	for _, v := range h.Values("Content-Security-Policy") {
		for _, d := range strings.Split(v, ";") {
			if f := strings.Fields(d); len(f) > 0 && strings.EqualFold(f[0], "frame-ancestors") {
				out = append(out, strings.Join(f, " "))
			}
		}
	}
	return out
}

func TestPageFramingIsSelfAndCannotBeLoosened(t *testing.T) {
	loosen := func(w http.ResponseWriter) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors *")
		w.Header().Set("X-Frame-Options", "ALLOWALL")
		w.Header().Set("Referrer-Policy", "unsafe-url")
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	cases := []struct {
		name    string
		respond func(http.ResponseWriter)
		ownCSP  bool
	}{
		{"handler writes nothing", func(http.ResponseWriter) {}, false},
		{"handler writes a body", writeOK, false},
		{"handler loosens, then writes", func(w http.ResponseWriter) { loosen(w); writeOK(w) }, true},
		{"handler loosens, then WriteHeader", func(w http.ResponseWriter) { loosen(w); w.WriteHeader(http.StatusAccepted) }, true},
		{"handler loosens, then flushes", func(w http.ResponseWriter) {
			loosen(w)
			if err := http.NewResponseController(w).Flush(); err != nil {
				panic(err)
			}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hits []pageHit
			p := newPlane(t, pageConfig(&hits, tc.respond))
			req := pageRequest(http.MethodGet, "/apps/tool/", "")
			req.AddCookie(login(t, p))
			rec := serve(p, req)
			if len(hits) != 1 {
				t.Fatalf("handler ran %d times, want 1", len(hits))
			}
			h := rec.Result().Header
			if got := cspFrameAncestors(h); !slices.Equal(got, []string{"frame-ancestors 'self'"}) {
				t.Errorf("frame-ancestors directives = %q, want exactly [frame-ancestors 'self']", got)
			}
			if got := h.Values("X-Frame-Options"); !slices.Equal(got, []string{"SAMEORIGIN"}) {
				t.Errorf("X-Frame-Options = %q, want [SAMEORIGIN]", got)
			}
			for k, want := range map[string]string{"Referrer-Policy": "no-referrer", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"} {
				if got := h.Values(k); !slices.Equal(got, []string{want}) {
					t.Errorf("%s = %q, want [%s]", k, got, want)
				}
			}
			if tc.ownCSP && !strings.Contains(strings.Join(h.Values("Content-Security-Policy"), ","), "default-src 'self'") {
				t.Errorf("the handler's own directives were dropped: %q", h.Values("Content-Security-Policy"))
			}
		})
	}
}

func TestEveryOtherPathKeepsTheFramingRefusal(t *testing.T) {
	var hits []pageHit
	p := newPlane(t, pageConfig(&hits, writeOK))
	session := login(t, p)
	for _, tc := range []struct {
		path    string
		session bool
	}{
		{"/ui/", true},
		{"/api/ui/panels", true},
		{"/api/ui/panels/table", true},
		{"/apps/none/", true},
		{"/apps/tool/", false}, // the 401 before the handler
		{"/login", false},
	} {
		req := pageRequest(http.MethodGet, tc.path, "")
		if tc.session {
			req.AddCookie(session)
		}
		h := serve(p, req).Result().Header
		if got := cspFrameAncestors(h); !slices.Equal(got, []string{"frame-ancestors 'none'"}) {
			t.Errorf("%s: frame-ancestors = %q, want ['none']", tc.path, got)
		}
		if got := h.Get("X-Frame-Options"); got != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", tc.path, got)
		}
	}
}

func TestPanelListNamesThePageForPagePanelsOnly(t *testing.T) {
	var hits []pageHit
	p := newPlane(t, pageConfig(&hits, writeOK))
	rec := send(p, http.MethodGet, "/api/ui/panels", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/ui/panels = %d", rec.Code)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %s, want 2", rec.Body)
	}
	if got := string(entries[0]["page"]); got != `"/apps/tool/"` {
		t.Errorf("tool page = %s, want \"/apps/tool/\"", got)
	}
	if _, has := entries[1]["page"]; has {
		t.Errorf("descriptor panel carries a page key: %s", rec.Body)
	}
	// The descriptor route has nothing to serve for a page panel.
	if rec := send(p, http.MethodGet, "/api/ui/panels/tool", "", true); rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/ui/panels/tool = %d %s, want 404", rec.Code, rec.Body)
	}
}

func TestPagePatternsAreMountedOnlyWithAPagePanel(t *testing.T) {
	var hits []pageHit
	with := newPlane(t, pageConfig(&hits, writeOK)).Patterns()
	for _, want := range []string{"GET /apps/{id}/", "POST /apps/{id}/", "PUT /apps/{id}/", "PATCH /apps/{id}/", "DELETE /apps/{id}/"} {
		if !slices.Contains(with, want) {
			t.Errorf("Patterns with a page panel lacks %q", want)
		}
	}
	for _, pat := range newPlane(t, baseConfig()).Patterns() {
		if strings.Contains(pat, "/apps/") {
			t.Errorf("Patterns with no page panel has %q", pat)
		}
	}
}

// credentialSeen is what the page handler saw of the plane's credentials.
type credentialSeen struct {
	authorization, cookies, query string
}

// The plane admits a request on its own credential and then hands the Page
// none of it: a page that logged or forwarded a request would otherwise
// leak the admin key, the HttpOnly session id or a ticket.
func TestPageNeverSeesThePlaneCredential(t *testing.T) {
	var seen []credentialSeen
	cfg := baseConfig()
	cfg.Panels = []sep2admin.Panel{{
		ID:                "tool",
		Label:             "Tool",
		Placement:         sep2admin.ExtensionSlot(1),
		DescriptorVersion: sep2admin.CurrentDescriptorVersion,
		Page: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var names []string
			for _, c := range r.Cookies() {
				names = append(names, c.Name+"="+c.Value)
			}
			seen = append(seen, credentialSeen{r.Header.Get("Authorization"), strings.Join(names, ","), r.URL.RawQuery})
		}),
	}}
	p := newPlane(t, cfg)
	session := login(t, p)

	tr := send(p, http.MethodPost, "/auth/ticket", "", true)
	var issued struct{ Ticket string }
	if err := json.Unmarshal(tr.Body.Bytes(), &issued); err != nil || issued.Ticket == "" {
		t.Fatalf("POST /auth/ticket = %d %s", tr.Code, tr.Body)
	}

	cases := []struct {
		name  string
		build func() *http.Request
	}{
		{"bearer", func() *http.Request {
			req := pageRequest(http.MethodGet, "/apps/tool/?keep=1", "")
			req.Header.Set("Authorization", "Bearer "+testKey)
			req.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
			return req
		}},
		{"session", func() *http.Request {
			req := pageRequest(http.MethodGet, "/apps/tool/?keep=1", "")
			req.AddCookie(session)
			req.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
			return req
		}},
		{"ticket", func() *http.Request {
			req := pageRequest(http.MethodGet, "/apps/tool/?ticket="+issued.Ticket+"&keep=1", "")
			req.AddCookie(&http.Cookie{Name: "theme", Value: "dark"})
			return req
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(seen)
			if rec := serve(p, tc.build()); rec.Code != http.StatusOK {
				t.Fatalf("GET = %d %s, want 200", rec.Code, rec.Body)
			}
			if len(seen) != before+1 {
				t.Fatalf("page handler ran %d times, want 1", len(seen)-before)
			}
			want := credentialSeen{"", "theme=dark", "keep=1"}
			if got := seen[len(seen)-1]; got != want {
				t.Fatalf("page saw %+v, want %+v", got, want)
			}
		})
	}
}

// A handler can write a header map key in any case, and net/http sends it
// as written, so the floor must replace every spelling, not only the
// canonical one.
func TestPageFloorReplacesNonCanonicalHeaderKeys(t *testing.T) {
	var hits []pageHit
	p := newPlane(t, pageConfig(&hits, func(w http.ResponseWriter) {
		h := w.Header()
		h["content-security-policy"] = []string{"frame-ancestors *"}
		h["x-frame-options"] = []string{"ALLOWALL"}
		h["x-content-type-options"] = []string{"sniff"}
		h["referrer-policy"] = []string{"unsafe-url"}
		h["cache-control"] = []string{"public, max-age=3600"}
		writeOK(w)
	}))
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/apps/tool/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "localhost"
	req.Header.Set("Authorization", "Bearer "+testKey)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET = %d, want 200", resp.StatusCode)
	}
	// The client canonicalizes what came over the wire, so a second line in
	// any case shows up as a second value here.
	for k, want := range map[string]string{
		"X-Frame-Options":        "SAMEORIGIN",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"Cache-Control":          "no-store",
	} {
		if got := resp.Header.Values(k); !slices.Equal(got, []string{want}) {
			t.Errorf("%s on the wire = %q, want [%s]", k, got, want)
		}
	}
	if got := cspFrameAncestors(resp.Header); !slices.Equal(got, []string{"frame-ancestors 'self'"}) {
		t.Errorf("frame-ancestors on the wire = %q, want exactly ['self']", got)
	}
}
