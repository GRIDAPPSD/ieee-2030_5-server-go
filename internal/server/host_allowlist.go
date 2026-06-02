package server

import (
	"log"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/discovery"
)

// IEEE-138: Host-header allowlist for the admin listener.
//
// The admin surface defaults to a loopback bind, and the IEEE-132 loopback
// bypass admits any request whose RemoteAddr is loopback when no proxy
// forwarded headers are present. A DNS-rebinding attacker can lure a
// browser to a page hosted at attacker-controlled DNS that resolves to
// 127.0.0.1; the browser then issues requests to the admin listener with
// `Host: evil.example.com`. Path 0 admits because RemoteAddr is loopback.
//
// HostAllowlistMiddleware is the defense-in-depth gate: it runs OUTSIDE the
// auth middleware and rejects any request whose Host header is not in the
// allowlist. The auth chain (Path 0 / A / B / C / D in internal/auth/admin.go)
// only runs for hosts the server claims.
//
// Status codes:
//   - missing Host header on HTTP/1.1 → 400 (RFC 7230 §5.4 says it's mandatory)
//   - empty Host on HTTP/1.0 or non-allowlisted Host on either → 421
//     Misdirected Request (RFC 7540 §9.1.2 — "this server doesn't claim
//     this hostname")
//
// Allowlist entries are matched against BOTH bare host and host:port forms
// of the request's Host header. `net.SplitHostPort` is the parser; if it
// errors (no port present), the whole Host value is treated as the bare
// host.
//
// IPv6 hosts: `[::1]` is the literal form in URLs and Host headers, and
// `net.SplitHostPort` strips the brackets. Allowlist entries for IPv6 use
// the bare `::1` form, not `[::1]`.
func HostAllowlistMiddleware(allowed []string) func(http.Handler) http.Handler {
	allowSet := make(map[string]struct{}, len(allowed))
	for _, h := range allowed {
		if h = strings.TrimSpace(h); h != "" {
			allowSet[strings.ToLower(h)] = struct{}{}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			host := r.Host

			// Empty Host on HTTP/1.1 is a protocol violation. Empty on
			// HTTP/1.0 is technically permitted but pointless on this
			// surface — we don't claim "any host", so 421 either way is
			// defensible. We pick 400 on HTTP/1.1 (per RFC 7230) and 421
			// on HTTP/1.0 (defense-in-depth).
			if host == "" {
				if r.ProtoMajor == 1 && r.ProtoMinor == 1 {
					log.Printf("admin: rejecting request with empty Host header (HTTP/1.1) from %s %s %s",
						r.RemoteAddr, r.Method, r.URL.Path)
					http.Error(w, "Bad Request: missing Host header", http.StatusBadRequest)
					return
				}
				log.Printf("admin: rejecting request with empty Host header (HTTP/%d.%d) from %s %s %s",
					r.ProtoMajor, r.ProtoMinor, r.RemoteAddr, r.Method, r.URL.Path)
				http.Error(w, "Misdirected Request", http.StatusMisdirectedRequest)
				return
			}

			bareHost := host
			if h, _, err := net.SplitHostPort(host); err == nil {
				bareHost = h
			}
			bareHost = strings.ToLower(bareHost)
			fullHost := strings.ToLower(host)

			// Match bare host OR full host:port form. Both are accepted so
			// allowlist entries authored as either shape work, and a
			// browser hitting `localhost:8444` matches an allowlist entry
			// of `localhost`.
			if _, ok := allowSet[bareHost]; ok {
				next.ServeHTTP(w, r)
				return
			}
			if _, ok := allowSet[fullHost]; ok {
				next.ServeHTTP(w, r)
				return
			}

			log.Printf("admin: rejecting request with disallowed Host header %q from %s %s %s (allowlist: %v)",
				host, r.RemoteAddr, r.Method, r.URL.Path, allowedKeys(allowSet))
			http.Error(w, "Misdirected Request", http.StatusMisdirectedRequest)
		})
	}
}

// DefaultAdminAllowedHosts returns the local-development default allowlist
// for the admin listener: loopback (v4 + v6), `localhost`, and the mDNS
// hostname (IEEE-133). The mDNS entry is derived from
// discovery.AdminHostname so a future rename of the mDNS hostname does not
// silently break the allowlist.
//
// These defaults are always installed; SEP2_ADMIN_ALLOWED_HOSTS appends to
// them rather than replacing them. There is no opt-out: the local-dev
// surface should never be silently disabled.
func DefaultAdminAllowedHosts() []string {
	return []string{
		"localhost",
		"127.0.0.1",
		"::1",
		discovery.AdminHostname,
	}
}

// ResolveAdminAllowedHosts merges the defaults with operator-supplied
// extras (deduplicated, case-insensitive). The result is what the
// middleware should be configured with.
func ResolveAdminAllowedHosts(extras []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(extras)+4)
	add := func(h string) {
		h = strings.TrimSpace(h)
		if h == "" {
			return
		}
		k := strings.ToLower(h)
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		out = append(out, h)
	}
	for _, h := range DefaultAdminAllowedHosts() {
		add(h)
	}
	for _, h := range extras {
		add(h)
	}
	return out
}

// allowedKeys returns the keys of m sorted lexicographically for log
// output. Map iteration order in Go is randomized, so the previous
// "insertion-stable order" claim was wrong — sorting gives diff-friendly
// log lines across runs and lets operators eyeball-compare allowlists
// from different boots.
func allowedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
