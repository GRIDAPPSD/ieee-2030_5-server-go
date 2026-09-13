package auth

import (
	"log/slog"
	"net/http"
)

// AdminCrossOriginMiddleware refuses a state-changing admin request that a
// browser marks as sent from another origin. It runs before any credential is
// consulted, because the loopback bypass and an installed admin client
// certificate both admit a request the operator never chose to send (#416).
// GET, HEAD and OPTIONS pass through; admin routes change no state on them.
func AdminCrossOriginMiddleware() func(http.Handler) http.Handler {
	protection := http.NewCrossOriginProtection()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Sec-Fetch-Site other than same-origin or none is refused. Without it,
			// Origin must match Host, and a request carrying neither is admitted:
			// curl and other tools send neither, and a browser that omits
			// Sec-Fetch-Site still sends Origin on a cross-origin POST or DELETE.
			if err := protection.Check(r); err != nil {
				slog.Warn("admin: cross-origin request refused",
					"event", "admin_cross_origin_refused",
					"method", r.Method,
					"path", r.URL.Path,
					"remote_addr", r.RemoteAddr,
					"sec_fetch_site", r.Header.Get("Sec-Fetch-Site"),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":"cross-origin admin request refused"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
