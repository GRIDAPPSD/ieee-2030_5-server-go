package adminplane

import (
	"net/http"
	"slices"
	"strings"
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
			pages[p.ID] = pageFloor(http.StripPrefix(strings.TrimSuffix(pageURL(p.ID), "/"), p.Page))
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
		h.ServeHTTP(w, r)
	}
	for _, pattern := range pagePatterns {
		authed.HandleFunc(pattern, serve)
	}
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
	csp := make([]string, 0, len(h.Values("Content-Security-Policy"))+1)
	for _, v := range h.Values("Content-Security-Policy") {
		if kept := withoutFrameAncestors(v); kept != "" {
			csp = append(csp, kept)
		}
	}
	h["Content-Security-Policy"] = append(csp, "frame-ancestors 'self'")
	h.Set("X-Frame-Options", "SAMEORIGIN")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
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
