package auth

import (
	"context"
	"crypto/subtle"
	"log"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/obs"
)

// forwardedHeaders lists the proxy-injected headers that, when present on a
// request, indicate the request was routed through a reverse proxy (e.g.
// Caddy). The #246 loopback bypass declines for any such request so the
// existing Bearer/cookie/mTLS auth chain runs against operator traffic.
var forwardedHeaders = []string{
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"Forwarded",
}

// AdminTicketCookieName is the cookie name used by the browser login flow
// (#159). The cookie carries a SessionStore id, which the middleware
// validates without consuming: one page load authenticates the document and
// every subresource it pulls separately, so a consuming check admits the
// first of them and refuses the rest.
const AdminTicketCookieName = "admin_ticket"

// AdminLoginPath is where an unauthenticated browser navigation is sent. The
// route is mounted on the public outer mux, outside this middleware, so the
// redirect cannot loop back into another refusal.
const AdminLoginPath = "/login"

// AdminRefusalVary lists the request headers wantsLoginPage reads. It is the
// Vary value on every refusal, in the order the decision consults them.
const AdminRefusalVary = "Sec-Fetch-Dest, Accept"

// AdminAuthMiddleware returns middleware that checks for admin authorization.
// Five paths are supported (checked in order):
//
//  0. Loopback bypass (#246): the request originated from a loopback
//     address (127.0.0.0/8 or ::1) AND no reverse-proxy forwarded header is
//     present. This is the local-developer ergonomic path: `make run` on
//     localhost has no working credentials by default, and Caddy in front
//     injects X-Forwarded-* so this bypass declines automatically and the
//     normal auth chain runs against operator traffic.
//  1. mTLS: client cert with admin policy OID (1.3.6.1.4.1.40732.2.5)
//  2. Bearer token: Authorization header matches adminKey
//  3. Cookie session: admin_ticket cookie validated against the
//     SessionStore without being consumed (browser login flow, #159).
//  4. Auth ticket: ?ticket= query param validated against the TicketStore
//     (short-lived, one-time-use - for browser SSE/EventSource clients)
//
// The ticket check runs last, after every other real credential, on purpose
// (#641 fix round 1, item 3): a ticket is the lowest-trust, shortest-lived of
// the four, so a caller who also holds an mTLS cert, a Bearer token, or a
// cookie session is admitted by that credential rather than the ticket. A
// ticket presented alongside one of those is still consumed (#641 fix round
// 2, item 2): one-time use holds regardless of which credential admits.
// Before this ordering, a cookie session presented alongside a
// ticket was admitted via the ticket (checked ahead of the cookie), which
// both spent the ticket needlessly and, on the one route that refuses
// ticket-only admission (RequireNonTicketAdmission), was refused a mint the
// same caller's cookie alone would have received.
//
// Paths 3 and 4 take separate stores on purpose. A ticket value is URL-borne
// and single-use; a session value is cookie-borne and reusable until it
// expires. Handing both to one store would make the values interchangeable
// and force one lifetime rule onto both.
//
// If adminKey is blank, Bearer auth is disabled (mTLS only). Blank means
// unset, empty, or whitespace-only: see IsBlankCredential.
// If tickets is nil, query-ticket auth is disabled; if sessions is nil,
// cookie auth is disabled.
//
// A refused request is answered by consumer: a browser navigating to a page is
// redirected to AdminLoginPath, and everything else gets a JSON 401 it can
// read. See wantsLoginPage.
func AdminAuthMiddleware(adminKey string, tickets *TicketStore, sessions *SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Path 0: loopback bypass (#246). Declines automatically
			// when ANY proxy-forwarded header is present so Caddy-fronted
			// deployments still run the full auth chain.
			if isLoopbackRemote(r) && !hasForwardedHeader(r) {
				log.Printf("admin: loopback bypass admitted %s %s", r.Method, r.URL.Path)
				next.ServeHTTP(w, withBypassAdmission(r))
				return
			}

			// Paths A through D: mTLS, Bearer, cookie session, ticket.
			if admits, path := credentialAdmits(r, adminKey, tickets, sessions); admits {
				next.ServeHTTP(w, withCredentialAdmission(r, path))
				return
			}

			// The refusal is negotiated: the same URL answers a navigation
			// and a subresource differently, so a cache that stored one and
			// replayed it to the other would deliver a login redirect into a
			// script tag. Vary names the request headers that decided it.
			w.Header().Set("Vary", AdminRefusalVary)

			// A browser has nothing to do with a 401 here: no current
			// browser prompts for a Bearer challenge, so a navigation that
			// receives one shows the operator a dead end. Send a navigation
			// to the login form and leave every other consumer the status it
			// reads.
			if wantsLoginPage(r) {
				http.Redirect(w, r, AdminLoginPath, http.StatusSeeOther)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"admin authentication required"}`))
		})
	}
}

// credentialPath names which of credentialAdmits's four paths admitted a
// request. Every consumer of the admission chain except AdmittedViaTicket
// treats all four as an interchangeable "real credential", by design; this
// type exists solely so the ticket-mint route can tell its own credential
// kind apart from the other three (#641).
type credentialPath int

const (
	credentialPathNone credentialPath = iota
	credentialPathMTLS
	credentialPathBearer
	credentialPathTicket
	credentialPathCookie
)

// credentialAdmits runs the four credentialed admission paths (mTLS, Bearer,
// cookie session, ticket), in the same order and with the same logging side
// effects AdminAuthMiddleware's own chain uses, and reports whether one of
// them admits r and, if so, which one. Extracted so RequireRealCredential
// (the sensitive-route recheck, #579 #631) and AdminAuthMiddleware's own
// chain share one implementation and can never drift on what counts as a
// real credential.
//
// The ticket check runs last, deliberately (#641 fix round 1, item 3): every
// other real credential admits a caller who also happens to carry a ticket,
// rather than the ticket taking precedence over a stronger credential. A
// ticket presented alongside one of those is still redeemed
// (redeemPresentedTicket, #641 fix round 2, item 2), so one-time use holds
// no matter which credential admits. See AdminAuthMiddleware's own doc comment for
// the failure this closes.
func credentialAdmits(r *http.Request, adminKey string, tickets *TicketStore, sessions *SessionStore) (bool, credentialPath) {
	// Path A: mTLS with admin OID
	if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
		cert := r.TLS.PeerCertificates[0]
		if certs.HasPolicyOID(cert, certs.OIDPolicyAdmin) {
			LogSuccessfulAdminCredential(r, obs.AdminAdmissionPathMTLS)
			redeemPresentedTicket(r, tickets)
			return true, credentialPathMTLS
		}
	}

	// Path B: Bearer token. A blank configured key disables the path
	// outright, so an operator who set only whitespace cannot be
	// authenticated by presenting the same whitespace. A blank presented
	// token is the same credential-free case as no Authorization header at
	// all, so only a non-blank mismatch logs a failure (#413).
	if !IsBlankCredential(adminKey) {
		if token, ok := bearerToken(r.Header.Get("Authorization")); ok && !IsBlankCredential(token) {
			if constantTimeEqual(token, adminKey) {
				LogSuccessfulAdminCredential(r, obs.AdminAdmissionPathBearer)
				redeemPresentedTicket(r, tickets)
				return true, credentialPathBearer
			}
			LogFailedAdminCredential(r, obs.AdminAdmissionPathBearer)
		}
	}

	// Path C: Cookie session (browser login flow, #159). Validation slides
	// the idle deadline but does not consume the session, and no cookie is
	// re-set here: a per-request rotation cannot survive the parallel
	// subresource loads of one page. Checked ahead of the ticket so a caller
	// who holds both is admitted by the reusable credential rather than the
	// one-time ticket (#641 fix round 1, item 3).
	if sessions != nil {
		if c, err := r.Cookie(AdminTicketCookieName); err == nil && c.Value != "" {
			if sessions.Validate(c.Value) {
				redeemPresentedTicket(r, tickets)
				return true, credentialPathCookie
			}
		}
	}

	// Path D: Short-lived auth ticket (for SSE/EventSource). Checked last:
	// it is the one credential kind a caller can leak in a URL, and the only
	// one this admission chain ever consumes on this path, so it is used
	// only when nothing else admits.
	if tickets != nil {
		if ticket := r.URL.Query().Get("ticket"); ticket != "" {
			if tickets.Redeem(ticket) {
				return true, credentialPathTicket
			}
		}
	}

	return false, credentialPathNone
}

// redeemPresentedTicket consumes a ticket on the query string even though a
// stronger credential (mTLS, Bearer, or a cookie session) is what admits r,
// so one-time use holds no matter which credential wins (#641 fix round 2,
// item 2; ticket.go's own comment calls this load bearing). The redemption
// result is discarded: admission already stands on the stronger credential,
// and an absent, expired, or already-spent ticket must not change that.
func redeemPresentedTicket(r *http.Request, tickets *TicketStore) {
	if tickets == nil {
		return
	}
	if ticket := r.URL.Query().Get("ticket"); ticket != "" {
		tickets.Redeem(ticket)
	}
}

// RequireRealCredential returns middleware for a route family that must
// never admit through the #246 loopback bypass alone (#579, #631): the
// certificate routes mint and return key material, and the traffic-capture
// read routes return captured Authorization and Cookie header bytes
// verbatim. It runs after AdminAuthMiddleware, which has already admitted
// every request that reaches it, bypass or credentialed.
//
// When AdmittedByLoopbackBypassOnly reports true, Path 0 short-circuited
// before any credential was consulted, so this middleware runs the same
// four credentialed paths AdminAuthMiddleware itself uses (credentialAdmits)
// against the same request: a credential that was present but never
// checked (a valid Bearer token sent alongside a loopback address, for
// instance) now admits, and the one-time ticket is consumed exactly once,
// here, since Path 0 never touches it. A request with nothing to check is
// refused and logged by LogSensitiveRouteRefusal.
//
// A request NOT marked bypass-only (admitted directly by mTLS, Bearer, a
// ticket, or a cookie session inside AdminAuthMiddleware) passes through
// unchanged: credentialAdmits never runs twice for the same request, so a
// ticket is never redeemed twice and a success log line is never doubled.
//
// A recheck that succeeds marks r with the path that admitted it, the same
// marker AdminAuthMiddleware leaves on a non-bypass admission, so a route
// that needs to tell the four credential kinds apart (AdmittedViaTicket, for
// the ticket-mint route, #641) sees the real one even when this recheck is
// what found it.
func RequireRealCredential(adminKey string, tickets *TicketStore, sessions *SessionStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if AdmittedByLoopbackBypassOnly(r) {
				admits, path := credentialAdmits(r, adminKey, tickets, sessions)
				if !admits {
					LogSensitiveRouteRefusal(r)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = w.Write([]byte(`{"error":"admin authentication required"}`))
					return
				}
				r = withCredentialAdmission(r, path)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// LogFailedAdminCredential records a presented-and-wrong admin credential at
// WARN, labeled by admissionPath (an obs.AdminAdmissionPath* constant). The
// signature takes no credential value, so there is nothing here a call site
// could log by mistake; callers must call this only after their own
// constant-time compare has already returned, never from inside a branch
// keyed on how much of the credential matched, so the log write itself
// carries no timing signal (#413).
func LogFailedAdminCredential(r *http.Request, admissionPath string) {
	slog.Warn("admin: credential presented and rejected",
		"event", "admin_auth_failure",
		"outcome", "wrong_credential",
		"admission_path", admissionPath,
		"method", r.Method,
		"path", r.URL.Path,
		"remote_addr", r.RemoteAddr,
	)
	obs.RecordAdminAuthFailure(admissionPath)
}

// LogSuccessfulAdminCredential records a successful credential PRESENTATION
// at INFO: the login form, a Bearer match, or an mTLS admission. Path C (the
// cookie session) is deliberately excluded from every caller: one page load
// validates the cookie four times for four independent subresources, so
// per-request success logging there would recreate the noise problem this
// closes on the failure side (#413).
func LogSuccessfulAdminCredential(r *http.Request, admissionPath string) {
	slog.Info("admin: credential presented and accepted",
		"event", "admin_auth_success",
		"admission_path", admissionPath,
		"method", r.Method,
		"path", r.URL.Path,
		"remote_addr", r.RemoteAddr,
	)
}

// admissionOutcomeContextKey marks HOW AdminAuthMiddleware admitted a
// request: through the #246 loopback bypass (Path 0, no credential of any
// kind presented) or through one of the four credentialed paths
// (credentialAdmits). AdmittedByLoopbackBypassOnly reads this marker.
//
// #579 MEDIUM-3: the marker used to be a single bool set only on the bypass
// path, so its ABSENCE (unset, or any request that never passed through
// AdminAuthMiddleware at all) read as "not bypass-only" and skipped the
// recheck. A future middleware that rebuilds the request context between
// AdminAuthMiddleware and RequireRealCredential, or any caller that wires
// RequireRealCredential up without AdminAuthMiddleware in front of it, would
// then admit silently with no credential and no log line. Recording both
// outcomes and treating anything that is not explicitly
// admissionOutcomeCredential as bypass-only makes that shape fail closed
// instead: an unmarked request is rechecked, not waved through.
type admissionOutcomeContextKey struct{}

type admissionOutcome int

const (
	admissionOutcomeCredential admissionOutcome = iota + 1
	admissionOutcomeBypass
)

// withBypassAdmission returns r marked as admitted by Path 0 alone.
func withBypassAdmission(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), admissionOutcomeContextKey{}, admissionOutcomeBypass))
}

// withCredentialAdmission returns r marked as admitted by one of the four
// credentialed paths (credentialAdmits), so RequireRealCredential's recheck
// knows not to run credentialAdmits a second time for the same request - a
// one-time ticket redeemed here must not be redeemed again. path records
// WHICH of the four admitted, for AdmittedViaTicket (#641); every other
// reader of the outcome marker still treats all four as interchangeable.
func withCredentialAdmission(r *http.Request, path credentialPath) *http.Request {
	ctx := context.WithValue(r.Context(), admissionOutcomeContextKey{}, admissionOutcomeCredential)
	ctx = context.WithValue(ctx, credentialPathContextKey{}, path)
	return r.WithContext(ctx)
}

// credentialPathContextKey holds the credentialPath a request was admitted
// by, set alongside admissionOutcomeContextKey whenever that outcome is
// admissionOutcomeCredential. AdmittedViaTicket is its only reader.
type credentialPathContextKey struct{}

// AdmittedViaTicket reports whether r's real credential - the one
// AdmittedByLoopbackBypassOnly already required before this runs - was Path
// D, the auth ticket itself, rather than mTLS, Bearer, or a cookie session.
// Every other consumer of the admission chain treats a ticket as an
// ordinary real credential; the ticket-mint route is the one place that
// must not (#641), because a ticket satisfying its own issuance route turns
// a 30-second leak into an unbounded one. An unmarked request (the path
// context key absent) reports false, the same fail-open-looking default
// credentialPathNone would give: callers only use this after establishing a
// real credential admitted at all, via AdmittedByLoopbackBypassOnly plus
// RequireRealCredential's own refusal, so an unmarked request never reaches
// here in practice.
func AdmittedViaTicket(r *http.Request) bool {
	v, _ := r.Context().Value(credentialPathContextKey{}).(credentialPath)
	return v == credentialPathTicket
}

// RequireNonTicketAdmission wraps the ticket-mint handler only
// (BuildAdminRouter, admin_router.go). It runs after AdminAuthMiddleware and
// RequireRealCredential have already established that some real credential
// admitted the request and left its kind on r's context (withCredentialAdmission).
// This is the one further refusal #641 needs: when that credential was
// itself a ticket, refuse, so a ticket can never mint its own successor.
// Every other real credential still mints one.
func RequireNonTicketAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if AdmittedViaTicket(r) {
			LogTicketSelfRenewalRefused(r)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"admin authentication required"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AdmittedByLoopbackBypassOnly reports whether r must be rechecked by
// RequireRealCredential: true unless AdminAuthMiddleware explicitly marked
// the request as admitted through a real credential. A route family that
// must not accept bypass-only admission (the certificate routes, the
// traffic-capture read routes, and the ticket-mint route, #579 and #631)
// checks this after AdminAuthMiddleware; a request carrying a real
// credential, from any address including loopback, reports false and is
// unaffected. An unmarked request - the marker absent entirely, not merely
// false - also reports true, per the fail-closed reasoning above.
func AdmittedByLoopbackBypassOnly(r *http.Request) bool {
	v, _ := r.Context().Value(admissionOutcomeContextKey{}).(admissionOutcome)
	return v != admissionOutcomeCredential
}

// LogSensitiveRouteRefusal records a refusal of a route that requires a real
// credential even from a loopback address (#579, #631): the request carried
// no credential at all, so this is distinct from LogFailedAdminCredential's
// "presented and wrong" case. Same field shape as the other admission log
// lines so all three are queryable the same way.
func LogSensitiveRouteRefusal(r *http.Request) {
	slog.Warn("admin: sensitive route refused bypass-only admission",
		"event", "admin_sensitive_route_refused",
		"outcome", "credential_required",
		"admission_path", "loopback_bypass",
		"method", r.Method,
		"path", r.URL.Path,
		"remote_addr", r.RemoteAddr,
	)
}

// LogTicketSelfRenewalRefused records RequireNonTicketAdmission's refusal
// (#641): the request DID carry a real credential, a ticket, so
// LogSensitiveRouteRefusal's "loopback_bypass" label would misdescribe this
// case as uncredentialed. Same field shape as the other admission log lines,
// with its own event name so the two refusal kinds stay distinguishable in a
// query.
func LogTicketSelfRenewalRefused(r *http.Request) {
	slog.Warn("admin: ticket-mint route refused ticket-only admission",
		"event", "admin_ticket_self_renewal_refused",
		"outcome", "credential_required",
		"admission_path", "ticket",
		"method", r.Method,
		"path", r.URL.Path,
		"remote_addr", r.RemoteAddr,
	)
}

// wantsLoginPage reports whether a refused request came from a browser
// navigating to a page, which is the only consumer that can act on a redirect.
// A script tag or a fetch() handed an HTML login page treats it as its own
// content type, so misreading a subresource as a navigation puts a login form
// inside a <script> and renders the shell blank.
func wantsLoginPage(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	// The JSON and SSE surface is called by code that reads the status, never
	// navigated to, and the SPA's own session probe depends on seeing the 401.
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/dashboard/") {
		return false
	}
	// Sec-Fetch-Dest is the browser's own statement of what the response is
	// for, and it separates a top-level document from the script, stylesheet
	// and icon the same page pulls. Current browsers send it on every request.
	if dest := r.Header.Get("Sec-Fetch-Dest"); dest != "" {
		return strings.EqualFold(dest, "document")
	}
	// A client that sends no Sec-Fetch-Dest falls back to Accept, where only an
	// explicit HTML media type counts: a script tag and fetch() both send */*.
	return acceptsHTML(r.Header.Get("Accept"))
}

// acceptsHTML reports whether an Accept header names an HTML media type
// explicitly. A wildcard does not count, and q=0 on the HTML type refuses it
// rather than asking for it.
func acceptsHTML(accept string) bool {
	for _, entry := range strings.Split(accept, ",") {
		params := strings.Split(entry, ";")
		switch strings.ToLower(strings.TrimSpace(params[0])) {
		case "text/html", "application/xhtml+xml":
			if !refusedByQuality(params[1:]) {
				return true
			}
		}
	}
	return false
}

// refusedByQuality reports whether a media type's parameters carry q=0.
func refusedByQuality(params []string) bool {
	for _, p := range params {
		name, value, ok := strings.Cut(p, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
			continue
		}
		q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		return err == nil && q == 0
	}
	return false
}

// NewAdminTicketCookie returns a Cookie carrying the given session id,
// configured with the security flags required for the browser login flow
// (HttpOnly, Secure, SameSite=Strict, Path=/). Only the login handler sets
// this cookie; the middleware never re-issues it.
func NewAdminTicketCookie(ticket string) *http.Cookie {
	return &http.Cookie{
		Name:     AdminTicketCookieName,
		Value:    ticket,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	}
}

// IsBlankCredential reports whether s carries no credential material.
// Unset, empty and whitespace-only are three distinct operator mistakes with
// one correct answer, so the boundary itself is a candidate for the condition
// it rejects: a key of " " must not authenticate a caller who presents " ".
//
// It is a blank/non-blank predicate only. It never returns a trimmed value,
// because trimming a non-blank key would change the secret the server accepts
// without telling the operator (#365).
func IsBlankCredential(s string) bool {
	return strings.TrimSpace(s) == ""
}

// bearerToken splits an Authorization header into the token68 payload that
// follows the "Bearer" scheme, reporting whether the header carried that
// scheme at all. The scheme token is compared case-insensitively per RFC 7235
// section 2.1, so "bearer" and "BEARER" reach the same comparison as "Bearer".
//
// Exactly one space separates the scheme from the payload and the payload is
// returned verbatim, never trimmed: a configured key whose value genuinely
// ends in whitespace is then matched byte-for-byte rather than silently
// mutated. HTTP header parsing strips surrounding whitespace from a field
// value on the wire, so such a key is effectively unpresentable over a real
// connection - configure a key with no leading or trailing whitespace.
func bearerToken(header string) (string, bool) {
	const scheme = "Bearer"
	if len(header) <= len(scheme) || header[len(scheme)] != ' ' {
		return "", false
	}
	if !strings.EqualFold(header[:len(scheme)], scheme) {
		return "", false
	}
	return header[len(scheme)+1:], true
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// isLoopbackRemote reports whether r.RemoteAddr is a loopback address (IPv4
// 127.0.0.0/8 or IPv6 ::1). httptest.NewRequest sets RemoteAddr to
// "192.0.2.1:1234" by default, so callers that exercise the loopback branch
// in tests must set RemoteAddr explicitly.
func isLoopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// RemoteAddr might be a bare IP (e.g. unix socket synthetics);
		// fall back to parsing the whole field.
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// hasForwardedHeader reports whether the request carries any reverse-proxy
// forwarded header (X-Forwarded-For, X-Forwarded-Host, X-Forwarded-Proto,
// or RFC 7239 Forwarded). An empty header value counts as not present so a
// proxy that strips the header can't accidentally enable the bypass.
func hasForwardedHeader(r *http.Request) bool {
	for _, name := range forwardedHeaders {
		if r.Header.Get(name) != "" {
			return true
		}
	}
	return false
}
