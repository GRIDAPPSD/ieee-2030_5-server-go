package store

import (
	"context"
	"fmt"
	"slices"
)

// Ordered returns s with [SortByListOrdering] served in the order compare
// defines. Every other SortKey, and every other method, goes to s unchanged.
//
// The ordering is applied here, over the whole collection and before paging,
// rather than trusted to s. That makes the served order independent of the
// order s returns rows in, which is the property section 4.6.1 needs from a
// backend that may be a query planner, and it is why Start and Limit are
// applied here too: a page cut by s in some other order holds the wrong
// members, and no sort of the page can fix that.
//
// After is ignored under SortByListOrdering. It is an id key here, while the
// wire's a parameter is a time bound on the primary key (section 4.6.2), so
// an id cut has no meaning within a field-based order.
func Ordered[T Copier[T]](s ResourceStore[T], compare func(a, b T) int) ResourceStore[T] {
	return orderedFlat[T]{ResourceStore: s, compare: compare}
}

// OrderedScoped is [Ordered] for the scoped half of the contract: List under
// each parent is served in the order compare defines.
func OrderedScoped[T Copier[T]](s ScopedStore[T], compare func(a, b T) int) ScopedStore[T] {
	return orderedScoped[T]{ScopedStore: s, compare: compare}
}

type orderedFlat[T Copier[T]] struct {
	ResourceStore[T]
	compare func(a, b T) int
}

func (o orderedFlat[T]) List(ctx context.Context, opts ListOptions) (ListResult[T], error) {
	if opts.Sort != SortByListOrdering {
		return o.ResourceStore.List(ctx, opts)
	}
	if err := opts.Validate(); err != nil {
		return ListResult[T]{}, err
	}
	whole, err := o.ResourceStore.List(ctx, ListOptions{Unbounded: true})
	if err != nil {
		return ListResult[T]{}, err
	}
	return pageInOrder(whole, o.compare, opts)
}

type orderedScoped[T Copier[T]] struct {
	ScopedStore[T]
	compare func(a, b T) int
}

func (o orderedScoped[T]) List(ctx context.Context, parentID string, opts ListOptions) (ListResult[T], error) {
	if opts.Sort != SortByListOrdering {
		return o.ScopedStore.List(ctx, parentID, opts)
	}
	if err := opts.Validate(); err != nil {
		return ListResult[T]{}, err
	}
	whole, err := o.ScopedStore.List(ctx, parentID, ListOptions{Unbounded: true})
	if err != nil {
		return ListResult[T]{}, err
	}
	return pageInOrder(whole, o.compare, opts)
}

// pageInOrder sorts an unbounded read and cuts the requested page from it.
//
// An unbounded read from the first item that returns fewer items than its own
// All is a broken backend, and sorting a partial collection would serve a
// confident wrong order, so it is an error rather than a page.
func pageInOrder[T any](whole ListResult[T], compare func(a, b T) int, opts ListOptions) (ListResult[T], error) {
	n := uint32(len(whole.Items))
	if whole.All != n {
		return ListResult[T]{}, fmt.Errorf("store: unbounded read returned %d of %d items; cannot order a partial collection", n, whole.All)
	}
	items := whole.Items
	slices.SortStableFunc(items, compare)

	start := min(opts.Start, n)
	items = items[start:]
	if !opts.Unbounded {
		items = items[:min(opts.Limit, uint32(len(items)))]
	}
	if len(items) == 0 {
		items = nil
	}
	return ListResult[T]{All: n, Results: uint32(len(items)), Items: items}, nil
}
