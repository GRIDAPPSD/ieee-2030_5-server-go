package server

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// #159: browser login flow for the admin surface.
//
// The login form is served at GET /login (unauthenticated) and posts to
// POST /auth/login (also unauthenticated). On a successful key match the
// server mints a SessionStore id and sets it as the admin_ticket cookie,
// then redirects to /. The middleware's Path D validates that cookie on
// each request without consuming it. This handler is the only place the
// cookie is set, so a client-supplied id is never adopted as a session.
//
// Only the admin key + cookie path is touched here. mTLS, Bearer, and
// query-param ticket auth keep their behavior.

// HandleLoginPage returns a handler for GET /login. errMsg is interpolated
// into the page; pass "" for the normal landing render. The handler always
// returns 200 — the form is the response.
func HandleLoginPage(errMsg string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		body := strings.Replace(loginHTML, "{{ERROR}}", htmlEscape(errMsg), 1)
		_, _ = w.Write([]byte(body))
	}
}

// HandleLoginSubmit returns a handler for POST /auth/login. Validates the
// posted "key" form field against adminKey with a constant-time compare. On
// success, mints a session via the supplied SessionStore and sets the
// admin_ticket cookie with HttpOnly + Secure + SameSite=Strict, then
// redirects to /. On wrong key, re-renders /login with an error message.
//
// If adminKey is blank the server is in mTLS-only mode and the login form is
// not a valid auth path; the handler returns 503. Blank is the same predicate
// the middleware's Bearer path uses (auth.IsBlankCredential), so a key of a
// single space cannot be refused on one credential path and accepted on this
// one.
func HandleLoginSubmit(adminKey string, sessions *auth.SessionStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if auth.IsBlankCredential(adminKey) || sessions == nil {
			http.Error(w, "browser login not available (no admin key configured)", http.StatusServiceUnavailable)
			return
		}

		if err := r.ParseForm(); err != nil {
			HandleLoginPage("Could not read form data.")(w, r)
			return
		}
		// A blank submission is refused before the compare, and a non-blank
		// one is compared byte-for-byte: the form value is never trimmed, so
		// a key whose own whitespace is part of the secret still matches.
		submitted := r.PostFormValue("key")
		if auth.IsBlankCredential(submitted) || !constantTimeEqual(submitted, adminKey) {
			w.WriteHeader(http.StatusOK)
			HandleLoginPage("Invalid admin key.")(w, r)
			return
		}

		id, err := sessions.Issue()
		if err != nil {
			log.Printf("login: issue admin session: %v", err)
			http.Error(w, "could not issue session ticket", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, auth.NewAdminTicketCookie(id))
		http.Redirect(w, r, "/", http.StatusSeeOther)
	}
}

// constantTimeEqual compares two strings in constant time. Duplicated here
// from internal/auth so server doesn't depend on auth's unexported helper;
// each comparison is small and the duplication is bounded.
func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// htmlEscape is a minimal HTML-attribute-safe escape for the error message
// embedded into the login template. Operator-supplied input doesn't reach
// this codepath — the message is server-controlled — but the substitution
// goes through DOM-as-string, so escape defensively.
func htmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(s)
}
