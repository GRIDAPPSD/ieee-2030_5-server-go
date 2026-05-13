package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/server"
)

// IEEE-095 login flow tests.

func TestLoginPageRendersForm(t *testing.T) {
	h := server.HandleLoginPage("")
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<form") || !strings.Contains(body, `action="/auth/login"`) {
		t.Errorf("login page must render a form posting to /auth/login")
	}
	if strings.Contains(body, "{{ERROR}}") && !strings.Contains(body, `class="error"></div>`) && !strings.Contains(body, `class="error"`) {
		// template token must be substituted, not raw
		t.Errorf("template ERROR placeholder must be substituted, got body containing %q", "{{ERROR}}")
	}
}

func TestLoginPageInjectsError(t *testing.T) {
	h := server.HandleLoginPage("Invalid key")
	req := httptest.NewRequest(http.MethodGet, "/login?err=1", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if !strings.Contains(w.Body.String(), "Invalid key") {
		t.Errorf("login error not injected, body=%s", w.Body.String())
	}
}

func TestLoginSubmitSuccessSetsCookieAndRedirects(t *testing.T) {
	tickets := auth.NewTicketStore(5 * time.Minute)
	h := server.HandleLoginSubmit("the-secret", tickets)

	form := url.Values{"key": []string{"the-secret"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 See Other, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("expected redirect to /, got %q", loc)
	}

	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.AdminTicketCookieName {
			cookie = c
			break
		}
	}
	if cookie == nil {
		t.Fatal("expected admin_ticket cookie to be set")
	}
	if !cookie.HttpOnly {
		t.Error("cookie must be HttpOnly")
	}
	if !cookie.Secure {
		t.Error("cookie must be Secure")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Error("cookie must be SameSite=Strict")
	}
	if cookie.Value == "" {
		t.Error("cookie value must be non-empty")
	}
}

func TestLoginSubmitWrongKey(t *testing.T) {
	tickets := auth.NewTicketStore(5 * time.Minute)
	h := server.HandleLoginSubmit("the-secret", tickets)

	form := url.Values{"key": []string{"wrong"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, req)

	// Render login page with an error (200) — do NOT set a cookie.
	if w.Code != http.StatusOK {
		t.Fatalf("wrong key should re-render login (200), got %d", w.Code)
	}
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.AdminTicketCookieName {
			t.Error("wrong key must not set admin_ticket cookie")
		}
	}
	if !strings.Contains(w.Body.String(), "Invalid") {
		t.Errorf("expected error message on wrong-key body, got %s", w.Body.String())
	}
}

func TestLoginSubmitEmptyKeyDisabled(t *testing.T) {
	tickets := auth.NewTicketStore(5 * time.Minute)
	// Server started with no admin key — login submission must be refused
	// (mTLS-only mode; no browser login possible).
	h := server.HandleLoginSubmit("", tickets)

	form := url.Values{"key": []string{"anything"}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("login submit with empty adminKey should 503, got %d", w.Code)
	}
}

func TestLoginSubmitFormParseError(t *testing.T) {
	tickets := auth.NewTicketStore(5 * time.Minute)
	h := server.HandleLoginSubmit("secret", tickets)
	// Body with broken urlencoded form (invalid percent escape) — ParseForm
	// returns an error and the handler must surface the login page rather
	// than a 5xx.
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader("key=%ZZ"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("form parse error should re-render login (200), got %d", w.Code)
	}
}

func TestLoginSubmitMethodNotAllowed(t *testing.T) {
	tickets := auth.NewTicketStore(5 * time.Minute)
	h := server.HandleLoginSubmit("secret", tickets)
	req := httptest.NewRequest(http.MethodGet, "/auth/login", nil)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /auth/login should be 405, got %d", w.Code)
	}
}
