// Package srverr is where every 500 and 400 a sep2srv handler raises is
// answered, so that every handler-raised failure of either kind carries a
// server-side record of why. The encoding layer (pkg/sep2/encoding) has its
// own 500 path for an XML encode failure, and that path does not carry this
// package's log prefix.
//
// # The operational problem this exists for
//
// A failure with no log line tells an operator that something went wrong and
// nothing about what. Before this package the handlers were uneven about it:
// some store failures logged and some did not, with no rule an operator or a
// reviewer could apply, so the presence of a log line said more about which
// afternoon a handler was written than about how serious the failure was.
//
// A durable backend attached behind these stores makes a 500 wall a real
// operational event: an outage produces 500s across the whole surface at
// once, and the operator's first symptom is a wall of identical 500 bodies.
// Whether that wall is a store outage or one client provoking errors on one
// route is answerable only from the server-side log, and only if every 500 is
// in it. This is not a someday concern about the in-memory stores this
// repository ships today: pkg/sep2srv/assembly/miswired.go's
// miswiredScopedStore and miswiredResourceStore already return a failure from
// every method they implement, reachable right now from any route wired to
// one, and familygate_test.go exercises exactly that path. A store that is
// merely wired to the wrong field is a reachable in-memory 500 with no
// backend involved at all.
//
// The 400 side has a different cause but the same operational shape. A
// decoder rejecting a malformed body is routine rather than a symptom of
// backend health, but its error text can quote the byte or element name that
// tripped it, which is client-supplied content this package's contract never
// lets reach a response body. [BadRequest] and [BadRequestMessage] hold every
// 400 to the same rule [Internal] holds every 500 to: the detail goes to the
// log, never to the client.
//
// # What is logged, and what is deliberately not
//
// One line per 500 or 400, carrying exactly two things: the ROUTE PATTERN
// that matched, and the error.
//
// The route pattern is [http.Request.Pattern], the registration string this
// server handed to its own ServeMux ("GET /edev/{id}/der/{derId}"). It is a
// server-side constant chosen at boot, never a value a client sent, and it
// names the failing route precisely enough to correlate a client-visible 500
// against the handler that emitted it.
//
// Nothing a client supplied is logged: not the request body, not a header,
// not the URL path, not a path value, not an identifier parsed out of a
// document. A log line is a place secrets leak, it is usually the least
// access-controlled artifact a server produces, and this codebase handles a
// registration pIN, device LFDIs and SFDIs, and client certificates. The
// concrete path is also the least useful of the options: it identifies one
// request, whereas the pattern identifies the route, and a store outage is a
// property of the route rather than of any one request.
//
// This costs something real and it is worth stating rather than glossing:
// the log usually does not say WHICH resource id failed. That is the trade
// the error itself is expected to cover, since a store implementation that
// cannot describe its own failure without the caller's identifiers has a
// reporting problem of its own. One exception: the EndDevice
// Registration-lookup error names the device's own Href, so that route's
// log line does say which resource failed.
package srverr

import (
	"log"
	"net/http"
)

// DefaultMessage is the response body written to the client for a 500 whose
// call site does not choose its own.
//
// It is deliberately uninformative. The client is not entitled to the
// server's internal failure detail, and the operator does not need the
// response body because the log line carries the detail instead.
const DefaultMessage = "internal error"

// UnroutedPattern is reported in place of the route when the request carries
// no pattern.
//
// A request served through this server's mux always carries one. A request
// that does not is a handler invoked directly, which in practice means a
// unit test calling the handler without a mux in front of it. Reporting a
// named placeholder rather than an empty string keeps such a line readable
// and keeps the field count of the line stable.
const UnroutedPattern = "(no route pattern)"

// logPrefix begins every line this package writes. It is a stable, greppable
// anchor: an operator filtering a mixed log for server-side failures, and the
// route-coverage test that asserts every 500 emits a line, both key off it.
const logPrefix = "sep2srv: 500 on "

// LogLinePrefix returns the leading portion of the line [Internal] writes for
// a 500 on route, up to and including the separator before the error.
//
// It exists so that a test asserting "every 500 emitted a log line naming its
// route" can key off the production format rather than off a copy of it. A
// copy would keep passing after the format changed, which is the failure mode
// where the check outlives the thing it checks.
func LogLinePrefix(route string) string {
	return logPrefix + route + ": "
}

// Route returns the mounted pattern that matched the request.
//
// It reads [http.Request.Pattern], which Go's ServeMux sets to the
// registration string of the matching route. That string comes from this
// server's own route table, so it is safe to log in a way the URL path is
// not.
func Route(r *http.Request) string {
	if r == nil || r.Pattern == "" {
		return UnroutedPattern
	}
	return r.Pattern
}

// Internal records err against the matched route and answers the request with
// a 500 carrying [DefaultMessage].
//
// Call it instead of writing http.Error with http.StatusInternalServerError
// directly. Routing every 500 through one function is what makes "every 500
// is logged" a property of the code rather than a habit each new handler has
// to be reminded of.
func Internal(w http.ResponseWriter, r *http.Request, err error) {
	InternalMessage(w, r, DefaultMessage, err)
}

// InternalMessage is [Internal] for the few call sites whose client-visible
// body is not [DefaultMessage].
//
// clientMessage is written to the client and MUST NOT carry internal detail;
// err is written to the log and never to the client.
func InternalMessage(w http.ResponseWriter, r *http.Request, clientMessage string, err error) {
	// A nil err would render as "<nil>", which reads like a bug in the
	// logging rather than like a call site that had no error value to
	// report, so it is named instead.
	detail := any("(no error reported)")
	if err != nil {
		detail = err
	}
	log.Printf("%s%s: %v", logPrefix, Route(r), detail)
	http.Error(w, clientMessage, http.StatusInternalServerError)
}
