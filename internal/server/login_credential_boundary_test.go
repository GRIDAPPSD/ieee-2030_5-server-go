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

// postLogin drives HandleLoginSubmit the way its caller does: a POST with a
// urlencoded form body, through the handler returned by the exported
// constructor. Nothing here reaches into the store or reimplements the compare.
func postLogin(t *testing.T, adminKey, submitted string) (*httptest.ResponseRecorder, *auth.SessionStore) {
	t.Helper()
	sessions := auth.NewSessionStore(30*time.Minute, 8*time.Hour)
	h := server.HandleLoginSubmit(adminKey, sessions)

	form := url.Values{"key": []string{submitted}}
	req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, req)
	return w, sessions
}

// hasSessionCookie reports whether the response set the admin_ticket cookie,
// which is the only thing that turns a login into an authenticated browser.
func hasSessionCookie(w *httptest.ResponseRecorder) bool {
	for _, c := range w.Result().Cookies() {
		if c.Name == auth.AdminTicketCookieName {
			return true
		}
	}
	return false
}

// TestLoginSubmitBlankConfiguredKeyMintsNoSession crosses the blank configured
// keys with the submissions an operator or attacker could send, and asserts no
// cell mints a session. The login form is a second credential path onto the
// same secret, so it must refuse exactly what the Bearer path refuses: a
// configured key of a single space cannot be admitted by submitting a space.
func TestLoginSubmitBlankConfiguredKeyMintsNoSession(t *testing.T) {
	t.Parallel()

	configured := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"single space", " "},
		{"tab", "\t"},
		{"newline", "\n"},
		{"mixed whitespace", " \t \n "},
	}

	submissions := []struct {
		name string
		// empty means the configured key is echoed back byte-for-byte
		value string
		echo  bool
	}{
		{"no key field", "", false},
		{"a single space", " ", false},
		{"a tab", "\t", false},
		{"an unrelated value", "anything", false},
		{"the configured key echoed back byte-for-byte", "", true},
	}

	if got, want := len(configured)*len(submissions), 25; got != want {
		t.Fatalf("login boundary table has %d cells (%d configured x %d submitted), want %d",
			got, len(configured), len(submissions), want)
	}

	for _, cfg := range configured {
		for _, sub := range submissions {
			t.Run(cfg.name+"/"+sub.name, func(t *testing.T) {
				t.Parallel()
				value := sub.value
				if sub.echo {
					value = cfg.key
				}
				w, sessions := postLogin(t, cfg.key, value)

				if hasSessionCookie(w) {
					t.Errorf("configured %q + submitted %q: a session cookie was set", cfg.key, value)
				}
				if got := sessions.Len(); got != 0 {
					t.Errorf("configured %q + submitted %q: %d sessions minted, want 0", cfg.key, value, got)
				}
				if w.Code == http.StatusSeeOther {
					t.Errorf("configured %q + submitted %q: redirected as if logged in", cfg.key, value)
				}
				// A blank configured key means mTLS-only mode, so the form is
				// not an auth path at all and the handler says so rather than
				// pretending the credential was merely wrong.
				if w.Code != http.StatusServiceUnavailable {
					t.Errorf("configured %q + submitted %q: status = %d, want 503", cfg.key, value, w.Code)
				}
			})
		}
	}
}

// TestLoginSubmitNonBlankKeyIsByteExact is the refusal in the other direction
// at this site: a configured key whose whitespace is part of the secret is
// accepted only when submitted byte-for-byte. The form value is not trimmed.
func TestLoginSubmitNonBlankKeyIsByteExact(t *testing.T) {
	t.Parallel()

	const configuredKey = "s3cret "

	cases := []struct {
		name        string
		submitted   string
		wantSession bool
	}{
		{"submitted byte-for-byte with the trailing space", "s3cret ", true},
		{"submitted trimmed", "s3cret", false},
		{"submitted with a second trailing space", "s3cret  ", false},
		{"submitted with a leading space instead", " s3cret", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w, sessions := postLogin(t, configuredKey, tc.submitted)

			if got := hasSessionCookie(w); got != tc.wantSession {
				t.Errorf("submitted %q: session cookie set = %v, want %v (status %d)",
					tc.submitted, got, tc.wantSession, w.Code)
			}
			wantLen := 0
			if tc.wantSession {
				wantLen = 1
			}
			if got := sessions.Len(); got != wantLen {
				t.Errorf("submitted %q: %d sessions minted, want %d", tc.submitted, got, wantLen)
			}
			if tc.wantSession && w.Code != http.StatusSeeOther {
				t.Errorf("submitted %q: status = %d, want 303", tc.submitted, w.Code)
			}
			if !tc.wantSession && w.Code != http.StatusOK {
				t.Errorf("submitted %q: status = %d, want 200 with the login page re-rendered", tc.submitted, w.Code)
			}
		})
	}
}
