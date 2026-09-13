// Package store defines the contract by which resource state attaches to an
// IEEE 2030.5 server.
//
// # This package is server-side state and is destined to move
//
// A resource store is server state. A 2030.5 client has no store: it issues
// requests and parses wire values. Under the four-layer target architecture
// (core is the shared library for client and server, server-go is the server,
// the bridge grafts onto the server), this package and its in-memory
// implementation belong in server-go, and they live in core today only because
// the server currently does. Nothing here is part of the shared
// client-and-server surface, and callers should not treat it as such.
//
// The relocation is sequenced with the layering split, not with this contract.
// See the bridge/core boundary analysis for the reasoning. Until the split
// lands, keep external dependencies on this package at zero so that the move
// stays a move rather than a breaking API change for three downstream modules.
//
// # The contract
//
// Four interfaces cross two axes, privilege and scope:
//
//   - [ResourceReader] is a flat collection, read-only.
//   - [ResourceStore] is a flat collection, read-write.
//   - [ScopedReader] is a parent-scoped collection, read-only.
//   - [ScopedStore] is a parent-scoped collection, read-write.
//
// The read/write split is load-bearing rather than decorative. A telemetry
// consumer and an administrative read surface need reads only, and handing
// either one a full store is over-privilege. Taking a reader handle makes
// "this path only reads" structural rather than conventional.
//
// The parent-scoped shape is the majority shape in 2030.5: DER resources,
// meter readings and function set assignments are all addressed as
// (parent, id) pairs.
//
// Deliberately absent from the contract is any equivalent of the in-memory
// implementation's ForParent, which returns a concrete per-parent store and
// materializes one on demand. Create-on-read is not implementable on a durable
// backend and turns a GET for an arbitrary path segment into an allocation.
// It remains available on the in-memory type, but nothing on its read half
// calls it any more: the reads there go through a lookup that
// creates nothing, so the contract's reading of an unknown parent, ErrNotFound
// from Get, an empty page from List, zero from Count, is now what the in-memory
// implementation does rather than what it emulates by allocating.
//
// # Paging, and the two things zero does not mean
//
// [ListOptions] carries the wire's paging parameters from section 4.6.2, so
// Limit is the l parameter and inherits its meaning exactly: l=0 asks for no
// items, which is a request a conformant client is entitled to make when it
// wants the total without the contents.
//
// That leaves no numeric value free to mean "every item". Zero is taken, and
// any other constant is a number a caller can reach by arithmetic on a count,
// at which point a request for a page silently becomes a request for the whole
// collection. [ListOptions.Unbounded] is a separate field for that reason: the
// unbounded read has to be named, and the two intents cannot be confused by a
// wrong number. A server-internal caller that wants everything under a parent
// asks for it directly, rather than passing a limit chosen to be larger than
// the data is expected to get.
//
// Setting both is refused rather than resolved. Honouring Limit would ignore
// an explicit request for everything, and honouring Unbounded would serve the
// whole collection to a caller that asked for a page: the same reasoning that
// makes an unrecognized [SortKey] an error instead of a fallback to the
// default order.
//
// # Copy semantics
//
// Implementations MUST be safe for concurrent use. Every value crossing the
// store boundary in either direction is an independent copy: a caller may
// mutate a resource it received, or one it passed to Create or Update, without
// affecting stored state. This is what the [Copier] constraint exists to
// guarantee, and it extends to reference-typed fields: a Copy implementation
// that shares a slice or map with its receiver does not satisfy it.
//
// # Error contract
//
// This contract is stated rather than implied because the in-memory
// implementation essentially cannot fail, so callers written against it have
// never met a fallible backend.
//
// These errors carry defined meaning:
//
//   - [ErrNotFound] means the addressed resource, or its parent, is absent.
//     On a request path this is a 404.
//   - [ErrAlreadyExists] means Create was called for an id already present.
//     The stored resource is unchanged.
//   - [ErrUnsupportedSort] means the requested [SortKey] is not one this
//     implementation can provide. It is never returned in place of silently
//     serving a different order.
//   - [ErrInvalidListOptions] means the [ListOptions] contradict themselves,
//     which is a caller bug rather than a backend condition. No client
//     request can produce it, so on a request path it is a 500.
//   - [ErrInvalidManagementPair] means [EndDeviceManagementStore.Assign] was
//     given a pair it will not record. It too is a caller error rather than a
//     backend condition.
//
// Match them with [errors.Is]: an implementation may wrap them with context.
//
// ANY other non-nil error is transient or unknown. A caller MUST surface it,
// which on a request path means a 500. It MUST NOT be flattened into
// not-found, and it MUST NOT be flattened into an empty result. An empty list
// and a failed lookup are different answers, and the error return is the only
// channel that distinguishes them: a failing List returns the same zero-valued
// [ListResult] that a legitimately empty collection does. Answering 200 with
// an empty list because a durable backend was briefly unreachable is a silent
// wrong answer on the wire.
//
// A caller that cannot complete a check MUST fail closed.
//
// The obligation runs in both directions, and neither half is optional. An
// IMPLEMENTATION must not report a failure as one of these sentinels, or as
// a nil error, because the value channel cannot carry the difference: a failed
// Get, a failed List and a failed Count each return exactly what the
// successful-and-empty case returns. A CONSUMER must not flatten a
// non-sentinel error into a 404, an empty list, or a synthesized default
// resource, because each of those is a claim about the fleet that a server
// whose backend stopped answering has not established. Only a 5xx says "I do
// not know", and that is the one answer a client retries.
//
// # How the contract is checked rather than asserted
//
// Both halves are enforced by tests rather than left to review.
//
//   - An implementation is checked by storetest.RunTransientFailureSuite,
//     which runs a healthy store and a failing one through the same calls and
//     pins that the error return discriminates where the value return cannot.
//   - Consumers are checked at the route level: the assembly package drives
//     EVERY mounted route through stores wrapped in storetest's fault
//     decorators and requires a 5xx from each, with the route list derived
//     from the router's own pattern enumeration so a route mounted tomorrow is
//     covered the day it appears rather than the day somebody remembers it.
//
// Both exist because the in-memory implementation essentially cannot fail, so
// no amount of exercising the current code reaches these branches. They go
// live all at once the day a durable backend is attached, which is the worst
// possible moment to be discovering which of them were never written.
//
// # Partial failure
//
// The contract defines no batch or transactional primitive. A caller writing
// several resources, such as a seeding pass over a fleet, may therefore
// half-fail: resources written before the error stay written. Callers that
// need all-or-nothing must implement it themselves, and callers that do not
// must be safe to re-run.
package store
