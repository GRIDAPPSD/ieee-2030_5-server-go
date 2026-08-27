package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server/web"
)

// distFS is the SPA's built static assets (internal/server/web/dist),
// rooted so its paths start at index.html rather than at dist/index.html.
// fs.Sub can only fail here if the embedded tree does not contain a
// "dist" directory, which cannot happen: web.DistFS's own
// "//go:embed all:dist" directive requires that directory to exist at
// compile time. A panic at package init on that impossible case is a
// build defect, not a runtime condition to handle gracefully.
var distFS = mustSubFS(web.DistFS, "dist")

func mustSubFS(f fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("server: web.DistFS is missing its \"" + dir + "\" root: " + err.Error())
	}
	return sub
}

// spaHandler serves the admin UI's built SPA. BuildAdminRouter mounts it
// under "/ui/" with the "/ui" prefix stripped, so every path this handler
// sees is rooted the same way distFS is.
//
// For a path that exists in distFS (an actual built asset, such as
// /assets/index-XXXX.js or /favicon.svg), the file server serves it
// directly. For any other non-/api path, index.html is served instead, so
// client side routing (internal/server/web/frontend/src/lib/router.ts)
// still works on a hard reload of a client side route.
//
// An /api path that reaches this handler unmatched 404s explicitly rather
// than falling through to index.html: a status code alone would not prove
// the SPA fallback was withheld, so the guard runs before the
// static-asset check, ahead of isStaticAsset.
func spaHandler() http.Handler {
	fileServer := http.FileServerFS(distFS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			// Best-effort write: headers are already sent, so a failure here
			// has nothing actionable to do beyond what the client already sees.
			_, _ = w.Write([]byte(`{"error":"not found"}`))
			return
		}

		if isStaticAsset(r.URL.Path) {
			fileServer.ServeHTTP(w, r)
			return
		}

		indexReq := r.Clone(r.Context())
		indexReq.URL.Path = "/"
		fileServer.ServeHTTP(w, indexReq)
	})
}

// isStaticAsset reports whether reqPath names a real, non-directory file
// in distFS. path.Clean normalizes the incoming request path before the
// lookup; an unrooted or traversal-shaped path (containing "..") fails
// fs.ValidPath inside distFS's Stat and is treated the same as "not a
// static asset", which routes it to the index.html fallback below rather
// than a filesystem error.
func isStaticAsset(reqPath string) bool {
	name := strings.TrimPrefix(path.Clean(reqPath), "/")
	if name == "" || name == "." {
		return false
	}
	info, err := fs.Stat(distFS, name)
	return err == nil && !info.IsDir()
}
