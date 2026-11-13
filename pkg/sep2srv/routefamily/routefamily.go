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
	// Anchor names the store whose presence mounts the family. The zero
	// value means the family is mounted whatever the deployment wires.
	Anchor Anchor
}

// Anchor names the store a deployment wires to serve a family.
type Anchor string

// The anchors a family can depend on. They match the store fields that gate
// the routes in the assembly package.
const (
	AnchorMirrorUsagePoints Anchor = "MirrorUsagePoints"
	AnchorUsagePoints       Anchor = "UsagePoints"
	AnchorMessagingPrograms Anchor = "MessagingPrograms"
	AnchorResponseSets      Anchor = "ResponseSets"
	AnchorDERs              Anchor = "DERs"
)

// Wired reports whether a deployment wires the store an anchor names.
type Wired func(Anchor) bool

// Serves reports whether the deployment serves the family at prefix: the
// prefix is in the table and its anchor, when it has one, is wired. A nil
// Wired serves only the families without an anchor.
func (w Wired) Serves(prefix string) bool {
	for _, f := range All() {
		if f.Prefix == prefix {
			return f.Anchor == "" || (w != nil && w(f.Anchor))
		}
	}
	return false
}

var (
	read     = []string{http.MethodGet, http.MethodHead}
	post     = []string{http.MethodPost}
	readPost = []string{http.MethodGet, http.MethodHead, http.MethodPost}
	// readPostDelete is /rsps: a Response is posted and, as mode O, deleted.
	readPostDelete = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete}
	readWrite      = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete}
)

// All returns the families. A path no family names is refused (Refuse).
// The build-tagged /test/mutations surface is deliberately outside this table.
func All() []Family {
	return []Family{
		{Prefix: "/dcap", Methods: read, Unauthenticated: true},
		{Prefix: "/tm", Methods: read, Unauthenticated: true},
		{Prefix: "/sdev", Subtree: true, Methods: read},
		{Prefix: "/edev", Subtree: true, Methods: readWrite},
		{Prefix: "/mup", Subtree: true, Methods: readWrite, Anchor: AnchorMirrorUsagePoints},
		{Prefix: "/dc", Subtree: true, Methods: read, Anchor: AnchorDERs},
		{Prefix: "/upt", Subtree: true, Methods: readWrite, Anchor: AnchorUsagePoints},
		{Prefix: "/rt", Subtree: true, Methods: read, Anchor: AnchorUsagePoints},
		{Prefix: "/msg", Subtree: true, Methods: readPost, Anchor: AnchorMessagingPrograms},
		{Prefix: "/rsps", Subtree: true, Methods: readPostDelete, Anchor: AnchorResponseSets},
		{Prefix: "/ntfy", Methods: post},
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
