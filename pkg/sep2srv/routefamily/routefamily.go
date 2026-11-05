// Package routefamily is the one table of top-level path families the
// protocol listener serves. The outer mount, the ACL rows and the href probe
// are all built from it, so a family cannot be mounted without an ACL row or
// given an ACL row without a mount. It imports nothing from the server, so the
// assembly package and the server's auth package can both depend on it.
package routefamily

import "net/http"

// Family is one top-level path family.
type Family struct {
	// Prefix is the family's root path, for example "/edev".
	Prefix string
	// Subtree also mounts Prefix + "/", so everything beneath the root is
	// reachable. A family without it serves the bare path only.
	Subtree bool
	// Methods are the HTTP methods the ACL allows on the family.
	Methods []string
	// Unauthenticated lets a client without a device certificate through the
	// ACL. Every family accepts a device certificate.
	Unauthenticated bool
}

var (
	read      = []string{http.MethodGet, http.MethodHead}
	readPost  = []string{http.MethodGet, http.MethodHead, http.MethodPost}
	readWrite = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete}
)

// All returns the families. A path no family names is refused (Refuse).
func All() []Family {
	return []Family{
		{Prefix: "/dcap", Methods: read, Unauthenticated: true},
		{Prefix: "/tm", Methods: read, Unauthenticated: true},
		{Prefix: "/sdev", Subtree: true, Methods: read},
		{Prefix: "/edev", Subtree: true, Methods: readWrite},
		{Prefix: "/mup", Subtree: true, Methods: readWrite},
		{Prefix: "/dc", Subtree: true, Methods: read},
		{Prefix: "/upt", Subtree: true, Methods: readPost},
		{Prefix: "/rt", Subtree: true, Methods: read},
		{Prefix: "/msg", Subtree: true, Methods: readPost},
		{Prefix: "/rsps", Subtree: true, Methods: readPost},
	}
}

// Mounts returns the outer-mux patterns that route the family to the protocol
// chain. net/http treats the bare path and the trailing-slash form as separate
// patterns: "/edev" matches only the collection, "/edev/" everything beneath.
func (f Family) Mounts() []string {
	if f.Subtree {
		return []string{f.Prefix, f.Prefix + "/"}
	}
	return []string{f.Prefix}
}

// Mounts returns every outer-mux pattern of every family.
func Mounts() []string {
	var out []string
	for _, f := range All() {
		out = append(out, f.Mounts()...)
	}
	return out
}

// RefusalBody is the body of the refusal Refuse writes. It is distinct from
// the ACL's "forbidden" so a conformance sweep can tell an unrouted path from
// an ACL denial on the wire.
const RefusalBody = "no route family names this path"

// Refuse answers a request under a path no family names. It uses the status
// the ACL gives a client whose authentication kind a rule does not accept, so
// an unknown path looks the same to every client kind, and it runs before any
// protocol handler.
func Refuse(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, RefusalBody, http.StatusForbidden)
}
