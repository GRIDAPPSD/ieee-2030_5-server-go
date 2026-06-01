package server

import (
	"net/http"
	"sort"
)

// routeRegistrar is the surface every register*Routes helper needs from
// a mux: the ability to attach an http.HandlerFunc against a (method-
// prefixed) pattern. *http.ServeMux satisfies it; *recordingMux below
// satisfies it AND captures the patterns for IEEE-140 boot logging.
// Keeping the surface thin (just HandleFunc) means new routes added
// via the helpers participate in the route enumeration without further
// plumbing.
type routeRegistrar interface {
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// recordingMux is a thin wrapper around *http.ServeMux that captures
// every pattern handed to HandleFunc / Handle. The Patterns method
// returns a sorted, de-duplicated copy of the captured strings — used
// by IEEE-140 to enumerate routes-per-listener at startup so a future
// /api/certs/* mis-mount surfaces in the boot log instead of becoming
// the next Leon CRITICAL.
//
// Sole writer is the constructor that builds the mux. Consumers read
// Patterns once at boot. No locking — boot is single-goroutine for
// router construction.
type recordingMux struct {
	mux      *http.ServeMux
	patterns []string
}

func newRecordingMux() *recordingMux {
	return &recordingMux{mux: http.NewServeMux()}
}

// HandleFunc registers a pattern with the underlying ServeMux and
// records it for Patterns(). Mirrors the *http.ServeMux signature so
// existing call sites flip with a single search-and-replace.
func (r *recordingMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.mux.HandleFunc(pattern, h)
	r.patterns = append(r.patterns, pattern)
}

// Handle registers a pattern with the underlying ServeMux and records
// it. Used for sub-handler attachment (e.g. mounting a sub-mux at a
// prefix). Mirrors *http.ServeMux's signature.
func (r *recordingMux) Handle(pattern string, h http.Handler) {
	r.mux.Handle(pattern, h)
	r.patterns = append(r.patterns, pattern)
}

// ServeHTTP delegates to the underlying mux so the recordingMux is
// itself an http.Handler.
func (r *recordingMux) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

// Patterns returns a sorted, de-duplicated copy of every registered
// pattern. Stable output keeps the boot log diff-friendly across runs.
// (A pattern registered against multiple HTTP methods on the SAME
// ServeMux is illegal — the stdlib panics — so duplicates here would
// only come from a second registration on the same recordingMux, which
// we collapse defensively.)
func (r *recordingMux) Patterns() []string {
	if len(r.patterns) == 0 {
		return nil
	}
	out := make([]string, len(r.patterns))
	copy(out, r.patterns)
	sortDedupePatterns(&out)
	return out
}

// sortDedupePatterns sorts in place and de-duplicates adjacent equal
// entries. Centralized so the protocol-listener and admin-listener
// route enumerators (BuildProtocolRouter and BuildAdminRouter) and
// recordingMux.Patterns produce byte-identical output shapes.
func sortDedupePatterns(p *[]string) {
	if len(*p) == 0 {
		return
	}
	s := *p
	sort.Strings(s)
	w := 0
	for i, v := range s {
		if i == 0 || v != s[w-1] {
			s[w] = v
			w++
		}
	}
	*p = s[:w]
}
