// Package srverr is where every 500 a sep2srv handler raises is answered, and
// where a 400 raised by a request-body decoder, or explicitly routed here by
// its handler, is answered too, so each carries a server-side record of why.
// It does not see every 400: see "What the 400 side covers, and what it does
// not" below for the boundary. The encoding layer (pkg/sep2/encoding) has its
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
// 400 that calls them to the same rule [Internal] holds every 500 to: the
// detail goes to the log, never to the client. Not every 400 a handler raises
// calls them; see the accounting below.
//
// # What is logged, and what is deliberately not
//
// One line per 500, and one line per 400 that reaches [BadRequest],
// [BadRequestMessage], or [LogBadRequest], carrying exactly two things: the
// ROUTE PATTERN that matched, and the error.
//
// The route pattern is [http.Request.Pattern], the registration string this
// server handed to its own ServeMux ("GET /edev/{id}/der/{derId}"). It is a
// server-side constant chosen at boot, never a value a client sent, and it
// names the failing route precisely enough to correlate a client-visible 500
// against the handler that emitted it.
//
// # What the 400 side covers, and what it does not
//
// A 400 produced by a request-body decoder (XML or JSON) reaches this
// package, and so does a 400 whose handler chooses to log it explicitly via
// [LogBadRequest] before writing its own response body. A 400 produced by a
// body-read failure (an oversized or truncated request) or by a plain
// request-shape refusal ahead of any decode (a missing path value, an empty
// required field, a malformed query parameter) does not: the handler writes
// its own fixed message directly and nothing is logged. At this package's
// last count, 21 such call sites in pkg/sep2srv answer a 400 this way; a
// change that routes one of them through this package updates that count.
//
// Nothing a client supplied is logged, with one bounded exception: not the
// request body, not a header, not the URL path, not a path value, not an
// identifier parsed out of a document. The exception is the 400 side's
// decoder error itself, which [BadRequest] and [BadRequestMessage] log as
// part of the record and which can quote a byte or element name the decoder
// rejected (see the 400 paragraph above); nothing beyond what the decoder
// itself echoes into its own error text is logged. A log line is a place
// secrets leak, it is usually the least access-controlled artifact a server
// produces, and this codebase handles a registration pIN, device LFDIs and
// SFDIs, and client certificates. The concrete path is also the least useful
// of the options: it identifies one request, whereas the pattern identifies
// the route, and a store outage is a property of the route rather than of any
// one request.
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

// logPrefix begins every line this package writes for a 500. It is a stable,
// greppable anchor: an operator filtering a mixed log for server-side
// failures, and the route-coverage test that asserts every 500 emits a line,
// both key off it.
const logPrefix = "sep2srv: 500 on "

// logPrefix400 is [logPrefix]'s 400 counterpart, so a decoder failure and a
// store failure are distinguishable in a mixed log without inspecting the
// status line that went with them.
const logPrefix400 = "sep2srv: 400 on "

// DefaultBadRequestMessage is the response body written to the client for a
// 400 whose call site does not choose its own.
//
// Like [DefaultMessage], it deliberately withholds the decoder's own error
// text: an XML or JSON syntax error can quote the byte or element name that
// tripped it, which is attacker-supplied content the client is not entitled
// to see reflected back, and the operator does not need it in the response
// because the log line carries it instead.
const DefaultBadRequestMessage = "invalid request"

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
	log.Printf("%s%s: %v", logPrefix, Route(r), errDetail(err))
	http.Error(w, clientMessage, http.StatusInternalServerError)
}

// BadRequest records err against the matched route and answers the request
// with a 400 carrying [DefaultBadRequestMessage].
//
// Call it instead of writing http.Error with http.StatusBadRequest and the
// decoder's own err.Error() directly. A decoder error can quote the input
// that failed to parse, and this package's doc says that content never
// reaches a client body; routing a decoder's 400 through this function is
// what keeps that a property of the code rather than a habit each new decode
// call site has to be reminded of. It does not cover every 400 a handler can
// raise; see the package doc's "What the 400 side covers" section.
func BadRequest(w http.ResponseWriter, r *http.Request, err error) {
	BadRequestMessage(w, r, DefaultBadRequestMessage, err)
}

// BadRequestMessage is [BadRequest] for call sites whose client-visible body
// is not [DefaultBadRequestMessage].
//
// clientMessage is written to the client and MUST NOT carry decoder detail;
// err is written to the log and never to the client.
func BadRequestMessage(w http.ResponseWriter, r *http.Request, clientMessage string, err error) {
	LogBadRequest(r, err)
	http.Error(w, clientMessage, http.StatusBadRequest)
}

// LogBadRequest records err against the matched route for a 400, without
// writing a response.
//
// It exists for the call sites [BadRequest] and [BadRequestMessage] cannot
// serve directly: a caller whose client-visible body is not plain text (the
// admin plane's JSON error envelope) still logs through this package's one
// convention, then writes its own response with its own fixed message.
func LogBadRequest(r *http.Request, err error) {
	log.Printf("%s%s: %v", logPrefix400, Route(r), errDetail(err))
}

// errDetail names a nil err rather than rendering it as "<nil>", which reads
// like a bug in the logging rather than like a call site that had no error
// value to report.
func errDetail(err error) any {
	if err != nil {
		return err
	}
	return "(no error reported)"
}
