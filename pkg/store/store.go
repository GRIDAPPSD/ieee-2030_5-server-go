package store

import (
	"context"
	"errors"
	"fmt"
)

// Copier is a constraint for types that can produce independent copies.
// All sep2 resource types must implement this for immutable store boundaries.
//
// Copy must be deep with respect to any field a caller could mutate: a Copy
// that shares a slice, map or pointer with its receiver breaks the store's
// copy semantics even though it satisfies this constraint.
type Copier[T any] interface {
	Copy() T
}

// ResourceReader is the read-only view of a flat resource collection.
//
// This is the handle a telemetry consumer or an administrative read surface
// should hold. It is the read half of [ResourceStore]; see the package
// documentation for copy semantics and the error contract.
//
// # Absent and failed are different answers, and every implementation MUST
// keep them so
//
// "This resource is not here" and "I could not find out" are distinct facts
// about the collection, and only the first is a statement an implementation is
// entitled to make on behalf of a backend that did not answer. An
// implementation MUST report the first as [ErrNotFound] and the second as some
// other non-nil error, and MUST NOT translate the second into the first, into a
// nil error, or into a zero value.
//
// The obligation is stated at every method below because the value channel
// cannot carry the difference: a failed Get returns the same zero-valued T that
// nothing else returns, a failed List returns the same zero-valued
// [ListResult], and a failed Count returns the same 0 an empty collection does.
// The error return is the only discriminator there is.
type ResourceReader[T Copier[T]] interface {
	// Get returns the resource with the given id, or ErrNotFound.
	//
	// ErrNotFound means the id is absent. A lookup that could not complete
	// MUST return its own error instead: on a request path the first is a 404
	// and the second is a 500, and a caller cannot tell them apart from the
	// zero-valued T that accompanies both.
	Get(ctx context.Context, id string) (T, error)

	// List returns one page of resources in the order named by opts.Sort.
	// An empty collection is not an error.
	//
	// Nor is a failure an empty collection. Both return a zero-valued
	// ListResult, so an implementation that reports a backend failure as a nil
	// error is telling its caller the collection is empty, and the caller will
	// serve that as a 200. There is no recovery downstream: the answer is
	// indistinguishable from the truth.
	List(ctx context.Context, opts ListOptions) (ListResult[T], error)

	// Count returns the number of resources in the collection.
	//
	// A count that could not be taken MUST be an error rather than 0. Zero is
	// a legitimate count, so a caller that receives it with a nil error is
	// entitled to act on it.
	Count(ctx context.Context) (uint32, error)
}

// ResourceStore provides generic CRUD and list operations for IEEE 2030.5
// resources held in a flat collection.
//
// Implementations MUST be safe for concurrent use. All reads return
// independent copies: callers may mutate what they receive without affecting
// the store.
//
// The write half carries the same obligation the read half does: a write that
// could not be attempted, and one that was refused because the collection is
// already in the state the sentinel names, are different answers. A nil error
// from any method here is a promise that the write took effect.
type ResourceStore[T Copier[T]] interface {
	ResourceReader[T]

	// Create stores resource under id. It returns ErrAlreadyExists if id is
	// already present, in which case the stored resource is unchanged.
	//
	// ErrAlreadyExists is a statement about the collection and callers branch
	// on it, typically into an Update. A failed Create MUST NOT borrow it: the
	// caller would take that branch and attempt a write against a backend that
	// is not answering, and a caller that treats it as "someone else got there
	// first" would report success for a write that never happened.
	Create(ctx context.Context, id string, resource T) error

	// Update replaces the resource stored under id. It returns ErrNotFound if
	// id is absent; Update never creates.
	//
	// A failed Update MUST NOT return ErrNotFound. On a request path that
	// renders as a 404, telling a client the resource it is holding does not
	// exist, which is a fact about the collection that the failure did not
	// establish.
	Update(ctx context.Context, id string, resource T) error

	// Delete removes the resource stored under id, or returns ErrNotFound.
	//
	// A failed Delete MUST report its own error. A caller that reads
	// ErrNotFound as "already gone", which is the ordinary and correct reading,
	// would otherwise stop reconciling a record that is still there.
	Delete(ctx context.Context, id string) error
}

// ScopedReader is the read-only view of a parent-scoped resource collection,
// the shape most of the 2030.5 resource surface uses: DER resources under a
// device, meter readings under a usage point, readings under a meter reading.
//
// Resource ids are unique within a parent, not across parents. The same id
// under two different parents addresses two different resources.
//
// The not-found-versus-failed obligation stated at [ResourceReader] applies
// here unchanged, and one scoped shape makes it sharper: an unknown parent
// yields an empty result and a nil error, so on this half of the contract
// "empty" is an answer the implementation gives ROUTINELY. That is exactly why
// a failure must not be flattened into it. A failing List here is not a rare
// degenerate case that looks like an empty one, it is identical to the
// commonest successful answer the contract has.
type ScopedReader[T Copier[T]] interface {
	// Get returns the resource stored under (parentID, id). It returns
	// ErrNotFound when either the parent or the resource is absent.
	//
	// A lookup that could not complete MUST return its own error: the parent
	// being unknown and the store being unreachable are different facts, and
	// only the first is one this call is entitled to assert.
	Get(ctx context.Context, parentID, id string) (T, error)

	// List returns one page of the resources under parentID, in the order
	// named by opts.Sort. An unknown parent yields an empty result and a nil
	// error, exactly as a known but empty parent does; the two are not
	// distinguished. A transient backend failure is NOT either of those and
	// MUST be reported as an error.
	List(ctx context.Context, parentID string, opts ListOptions) (ListResult[T], error)

	// Count returns the number of resources under parentID. An unknown parent
	// counts zero and is not an error. A count that could not be taken is an
	// error and MUST NOT be reported as zero.
	Count(ctx context.Context, parentID string) (uint32, error)

	// HasParent reports whether the store knows parentID.
	//
	// It MUST report true for any parent holding at least one resource.
	// Whether it keeps reporting true after the last resource under that
	// parent is removed is implementation-defined, as is whether a parent
	// that has only ever been read is reported: callers must not infer
	// emptiness from it.
	//
	// It is fallible on purpose. On a durable backend this is a query, not a
	// cheap local map lookup, and a caller that cannot complete the check must
	// fail closed rather than assume absence.
	HasParent(ctx context.Context, parentID string) (bool, error)

	// Parents returns the parent ids the store knows, in ascending
	// lexicographic order. Every parent holding at least one resource MUST
	// appear; an implementation MAY additionally report parents it has
	// materialized but which hold nothing.
	//
	// This exists because a consumer that needs "which devices have DER
	// status" cannot otherwise ask: HasParent only answers for a parent the
	// caller already named, which restricts such a consumer to enumerating
	// parents out of some other store and probing one at a time.
	//
	// An enumeration that could not be taken MUST be an error rather than an
	// empty slice. A consumer that reads an empty slice concludes the fleet
	// holds nothing of this kind and acts on it, which is the mistake the
	// bridge already had to add a fail-loud guard against on the CIM side.
	Parents(ctx context.Context) ([]string, error)
}

// ScopedStore provides CRUD and list operations for resources addressed as
// (parent, id) pairs.
//
// Implementations MUST be safe for concurrent use and return independent
// copies, exactly as [ResourceStore] does, and MUST keep a failed write
// distinguishable from a refused one, exactly as [ResourceStore] does.
type ScopedStore[T Copier[T]] interface {
	ScopedReader[T]

	// Create stores resource under (parentID, id), establishing the parent if
	// the implementation requires one. It returns ErrAlreadyExists if id is
	// already present under that parent.
	//
	// A failed Create MUST NOT return ErrAlreadyExists. The upsert idiom in
	// this repository branches on it straight into an Update, so borrowing it
	// for a failure turns one failed write into two.
	Create(ctx context.Context, parentID, id string, resource T) error

	// Update replaces the resource stored under (parentID, id). It returns
	// ErrNotFound when either the parent or the resource is absent, and never
	// creates either.
	//
	// A failed Update MUST report its own error rather than ErrNotFound.
	Update(ctx context.Context, parentID, id string, resource T) error

	// Delete removes the resource stored under (parentID, id), or returns
	// ErrNotFound.
	//
	// A failed Delete MUST report its own error rather than ErrNotFound, which
	// a caller reads as "already gone" and stops reconciling.
	Delete(ctx context.Context, parentID, id string) error
}

// SortKey selects the order in which [ResourceReader.List] and
// [ScopedReader.List] return resources.
//
// Ordering is part of the contract rather than an implementation detail.
// IEEE 2030.5 section 4.6.1 requires each list resource to be returned in a
// defined order, and the paging parameters address positions within that
// order, so a page number means nothing without it. Today the ordering is an
// emergent property of the in-memory store's sorted key slice: a durable
// backend would be free to return rows in whatever order the query planner
// chose, satisfy every method signature here, and silently fail the list
// ordering requirements (DER-PRG-001 through DER-PRG-004, MTR-GEN-010).
type SortKey uint8

const (
	// SortByIDAsc orders by resource id, ascending in byte order. It is the
	// zero value, so it is what a caller that says nothing about ordering
	// gets, and every implementation MUST support it.
	SortByIDAsc SortKey = iota

	// SortByIDDesc orders by resource id, descending in byte order. An
	// implementation that cannot provide it MUST return ErrUnsupportedSort
	// rather than serve a different order.
	SortByIDDesc
)

// String implements fmt.Stringer.
func (k SortKey) String() string {
	switch k {
	case SortByIDAsc:
		return "id-asc"
	case SortByIDDesc:
		return "id-desc"
	default:
		return "unknown"
	}
}

// ListOptions specifies paging parameters per IEEE 2030.5 section 4.6.2 and
// the ordering they are relative to, per section 4.6.1.
type ListOptions struct {
	Start uint32 // s param: first ordinal position (0-based)
	Limit uint32 // l param: max items to return; 0 returns no items
	After string // a param: return items ordered strictly after this key

	// Sort names the order the page is taken from. The zero value,
	// SortByIDAsc, is the required baseline; Start and After are interpreted
	// relative to it. An unrecognized value yields ErrUnsupportedSort.
	Sort SortKey

	// Unbounded requests every item from the starting position onward,
	// with no page size at all.
	//
	// It is a separate field because Limit cannot express it. Limit is the
	// wire's l parameter, and l=0 is a conformant request that means "no
	// items": a client may legitimately ask for the total without the
	// contents. So zero cannot be overloaded to mean "everything", and no
	// other numeric value can carry it either, because a caller can arrive at
	// any particular number by arithmetic on a count and would then silently
	// receive the whole collection instead of a page. Keeping the two intents
	// in separate fields makes "everything" something a caller has to name
	// and cannot compute into.
	//
	// It is for server-internal reads over a collection the server itself
	// bounds, such as assembling a snapshot of a known-small fleet. It is
	// never set from a request: a page size that arrived on the wire belongs
	// to the client, which is why Validate rejects Unbounded together with a
	// non-zero Limit rather than picking one of them.
	//
	// The zero value is false, so a caller that says nothing about paging
	// still gets the previous meaning of ListOptions{}: no items.
	Unbounded bool
}

// Validate reports whether the options are self-consistent, independent of any
// implementation. Implementations MUST call it before serving a List and MUST
// NOT serve a request it rejects.
//
// It deliberately does not check Sort. Which SortKey values an implementation
// can provide is implementation-specific, and ErrUnsupportedSort is that
// implementation's answer to give, not this package's.
func (o ListOptions) Validate() error {
	if o.Unbounded && o.Limit != 0 {
		return fmt.Errorf("%w: Unbounded set with Limit %d; an unbounded read takes no page size", ErrInvalidListOptions, o.Limit)
	}
	return nil
}

// ListResult contains a page of results with total count metadata.
type ListResult[T any] struct {
	// All is the total number of matching resources, independent of paging.
	// Unbounded is a paging choice like any other, so it does not change All:
	// an unbounded List reports the same All that a one-item page of the same
	// collection reports.
	//
	// Results therefore equals All on an unbounded List only when the page
	// also began at the first item. An unbounded List with a Start offset or
	// an After key reports Results below All by exactly the number of items
	// those skipped.
	All uint32

	Results uint32 // count returned in this page
	Items   []T
}

// Sentinel errors for store operations. These four, and only these four,
// carry defined meaning; see the package documentation for the full error
// contract. Match with errors.Is: implementations may wrap them.
var (
	ErrNotFound        = errors.New("resource not found")
	ErrAlreadyExists   = errors.New("resource already exists")
	ErrUnsupportedSort = errors.New("unsupported sort key")

	// ErrInvalidListOptions means the ListOptions contradict themselves and
	// no page can be served for them. It reports a bug in the calling code
	// rather than any condition of the backend or of the request: nothing a
	// client can send produces it, so on a request path it is a 500.
	ErrInvalidListOptions = errors.New("invalid list options")
)
