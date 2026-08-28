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
// A path under /ui/api/ (this handler's own mount point plus "/api")
// that reaches here unmatched 404s explicitly rather than falling
// through to index.html: a status code alone would not prove the SPA
// fallback was withheld, so the guard runs ahead of isStaticAsset. The
// real /api/* surface is registered separately on the same mux and
// never reaches this handler.
func spaHandler() http.Handler {
	fileServer := distFileServer()
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

		serveSPAIndexWith(fileServer, w, r)
	})
}

// distFileServer serves the embedded SPA tree. Constructed per caller
// rather than shared in a package var so nothing can swap it at runtime.
func distFileServer() http.Handler {
	return http.FileServerFS(distFS)
}

// serveSPAIndex writes the built index.html for a request whose path is
// not itself an asset. The dashboard route at "/" uses this to serve the
// admin UI, so the SPA is reachable at the address an operator types
// without a redirect, and the SPA's own /ui/ mount keeps working because
// every built asset URL in index.html is absolute (/ui/assets/...).
func serveSPAIndex(w http.ResponseWriter, r *http.Request) {
	serveSPAIndexWith(distFileServer(), w, r)
}

// serveSPAIndexWith rewrites the request path to the dist root so the
// file server answers with index.html rather than 404ing on a client side
// route. The request is cloned: mutating the caller's URL would corrupt
// any later handler or log line that reads the original path.
func serveSPAIndexWith(fileServer http.Handler, w http.ResponseWriter, r *http.Request) {
	indexReq := r.Clone(r.Context())
	indexReq.URL.Path = "/"
	fileServer.ServeHTTP(w, indexReq)
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
