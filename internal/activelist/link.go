package activelist

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// Counter is the read-only subset of a store a program decorator needs to
// count the active members of one scope.
type Counter interface {
	Count(ctx context.Context, parentID string) (uint32, error)
}

// StampLink serves a program's active list link from the live count of scope.
// A stored *link keeps its href and has its All recomputed; an absent one is
// built from derive, and stays absent when derive reports false. A failed
// count clears *link, so no wrong All is served, and returns the error for the
// caller to log with its own set's wording. It lives here beside the filter
// that defines "active" so every function set stamps by one sequence and only
// the scope key and href derivation differ.
func StampLink(ctx context.Context, counter Counter, scope string, link **sep2.ListLink, derive func() (string, bool)) error {
	n, err := counter.Count(ctx, scope)
	if err != nil {
		*link = nil
		return err
	}
	if *link == nil {
		href, ok := derive()
		if !ok {
			return nil
		}
		*link = &sep2.ListLink{Href: href}
	}
	(*link).All = n
	return nil
}
