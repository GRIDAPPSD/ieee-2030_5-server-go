package adminplane

import (
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
)

// pagePrefix is where a page panel is mounted: /apps/{id}/.
const pagePrefix = "/apps/"

// pageMethods are the methods a page panel is mounted for. One pattern
// without a method would conflict with the dashboard's "GET /" in
// net/http's mux, so each method is its own pattern; GET covers HEAD.
var pageMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

// pagePatterns are the authed-mux patterns mountPages registers.
var pagePatterns = func() []string {
	out := make([]string, len(pageMethods))
	for i, m := range pageMethods {
		out[i] = m + " " + pagePrefix + "{id}/"
	}
	return out
}()

func isPagePattern(pattern string) bool { return slices.Contains(pagePatterns, pattern) }

// isPagePath reports whether a request path falls under the page mount. It
// is read from the raw request path, before the mux cleans it, so it can
// only over-match a page request, never miss one.
func isPagePath(path string) bool {
	return strings.HasPrefix(path, pagePrefix)
}

// pageURL is the path the panel list gives the shell for a page panel.
func pageURL(id string) string { return pagePrefix + id + "/" }

// mountPages mounts every page panel under /apps/{id}/ on the authed mux.
// With no page panel nothing is mounted, so a plane without one keeps its
// route list. Not a mountWrite: like a panel action, a page acts on its
// embedder, not on the plane's stores, so ReadOnly does not filter it.
func (ps *panelSet) mountPages(authed *recordingMux) {
	pages := map[string]http.Handler{}
	for _, p := range ps.panels {
		if p.Page != nil {
			pages[p.ID] = pageFloor(withoutCredentials(http.StripPrefix(strings.TrimSuffix(pageURL(p.ID), "/"), p.Page)))
		}
	}
	if len(pages) == 0 {
		return
	}
	serve := func(w http.ResponseWriter, r *http.Request) {
		h, ok := pages[r.PathValue("id")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		// A ticket is one-time and travels in a URL; it may open a page but
		// must not change state through one, as with a panel action.
		if r.Method != http.MethodGet && r.Method != http.MethodHead && auth.AdmittedViaTicket(r) {
			log.Printf("admin: page %q: refused reason=ticket method=%s status=%d admission=%s remote=%s",
				r.PathValue("id"), r.Method, http.StatusUnauthorized, auth.AdmissionPath(r), r.RemoteAddr)
			writePanelError(w, http.StatusUnauthorized, "page writes do not accept a ticket")
			return
		}
		h.ServeHTTP(w, r)
	}
	for _, pattern := range pagePatterns {
		authed.HandleFunc(pattern, serve)
	}
}

// withoutCredentials hands next a copy of the request carrying none of the
// plane's credentials: no Authorization header, no admin_ticket cookie and
// no ticket query parameter. Admission has already happened, and a page
// that logged or forwarded its request would otherwise leak the admin key,
// the HttpOnly session id or a ticket.
func withoutCredentials(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r2 := r.Clone(r.Context())
		r2.Header.Del("Authorization")
		if cookies := keptCookies(r2.Header.Values("Cookie")); cookies == "" {
			r2.Header.Del("Cookie")
		} else {
			r2.Header.Set("Cookie", cookies)
		}
		r2.URL.RawQuery = withoutTicket(r2.URL.RawQuery)
		r2.RequestURI = r2.URL.RequestURI()
		next.ServeHTTP(w, r2)
	})
}

// keptCookies joins every cookie pair but admin_ticket, keeping each pair's
// bytes as sent.
func keptCookies(lines []string) string {
	var kept []string
	for _, line := range lines {
		for _, pair := range strings.Split(line, ";") {
			pair = strings.TrimSpace(pair)
			name, _, _ := strings.Cut(pair, "=")
			if pair == "" || strings.TrimSpace(name) == auth.AdminTicketCookieName {
				continue
			}
			kept = append(kept, pair)
		}
	}
	return strings.Join(kept, "; ")
}

// withoutTicket drops every ticket parameter from a raw query and keeps the
// rest as sent. Keys are compared decoded, as the auth chain's own
// url.Values lookup decodes them; a key that does not decode is one that
// lookup skips, so it cannot be the ticket.
func withoutTicket(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	var kept []string
	for _, pair := range strings.Split(rawQuery, "&") {
		key, _, _ := strings.Cut(pair, "=")
		if k, err := url.QueryUnescape(key); err == nil && k == "ticket" {
			continue
		}
		kept = append(kept, pair)
	}
	return strings.Join(kept, "&")
}

// pageFloor wraps a page handler so its response carries the plane's header
// floor whatever the handler set: framing relaxed to same-origin only, and
// the sniff, referrer and cache refusals every admin response has. The
// floor is applied when the header is committed, after the handler has had
// its chance to set headers, so a handler cannot loosen it.
func pageFloor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fw := &floorWriter{ResponseWriter: w}
		next.ServeHTTP(fw, r)
		// A handler that wrote nothing has its header committed by the
		// server after this returns, from the same map.
		fw.commit()
	})
}

// floorWriter applies the page header floor before the header reaches the
// wire: at WriteHeader, the first Write, or a Flush.
type floorWriter struct {
	http.ResponseWriter
	committed bool
}

func (w *floorWriter) commit() {
	if !w.committed {
		applyPageFloor(w.Header())
	}
}

func (w *floorWriter) WriteHeader(code int) {
	// A 1xx sends the header as it stands and leaves it open, so the floor
	// is applied each time and only a final status closes it.
	applyPageFloor(w.Header())
	if code >= 200 || code == http.StatusSwitchingProtocols {
		w.committed = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *floorWriter) Write(b []byte) (int, error) {
	w.commit()
	w.committed = true
	return w.ResponseWriter.Write(b)
}

// FlushError is what http.ResponseController calls; Flush serves the plain
// http.Flusher interface, which has no way to return the error.
func (w *floorWriter) FlushError() error {
	w.commit()
	w.committed = true
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func (w *floorWriter) Flush() { _ = w.FlushError() }

// Unwrap lets http.ResponseController reach the underlying writer for the
// controls floorWriter does not define (deadlines, full duplex).
func (w *floorWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// applyPageFloor sets the page subtree's headers on h. The handler's own
// Content-Security-Policy is kept, minus any frame-ancestors directive, with
// frame-ancestors 'self' added, so the page keeps its script and style
// policy but can be framed only by its own origin. X-Frame-Options has no
// per-origin form beyond SAMEORIGIN, which matches.
func applyPageFloor(h http.Header) {
	handlerCSP := takeAnyCase(h, "Content-Security-Policy")
	csp := make([]string, 0, len(handlerCSP)+1)
	for _, v := range handlerCSP {
		if kept := withoutFrameAncestors(v); kept != "" {
			csp = append(csp, kept)
		}
	}
	h["Content-Security-Policy"] = append(csp, "frame-ancestors 'self'")
	for _, k := range []string{"X-Frame-Options", "X-Content-Type-Options", "Referrer-Policy", "Cache-Control"} {
		takeAnyCase(h, k)
	}
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
}

// takeAnyCase removes every key of h that spells name in any case and
// returns their values. A handler may write the map directly, and net/http
// sends a non-canonical key as its own header line, so replacing only the
// canonical key would leave the handler's line on the wire beside the floor.
func takeAnyCase(h http.Header, name string) []string {
	var values []string
	for k, v := range h {
		if strings.EqualFold(k, name) {
			values = append(values, v...)
			delete(h, k)
		}
	}
	return values
}

// withoutFrameAncestors returns policy with every frame-ancestors directive
// removed. Directive names are case-insensitive (CSP3 section 2.2).
func withoutFrameAncestors(policy string) string {
	var kept []string
	for _, d := range strings.Split(policy, ";") {
		f := strings.Fields(d)
		if len(f) == 0 || strings.EqualFold(f[0], "frame-ancestors") {
			continue
		}
		kept = append(kept, strings.TrimSpace(d))
	}
	return strings.Join(kept, "; ")
}
