package store

import (
	"context"
	"fmt"
)

// ParentCascader is the optional capability to remove a whole parent
// collection in one operation and report how many resources went with it.
//
// It is not part of [ScopedStore]: the contract addresses (parent, id) pairs
// and offers no key enumeration, so bulk removal is a capability an
// implementation either has or does not. Callers use [CascadeParent] and
// [ProbeParent] rather than asserting it themselves, so every cascade refuses
// the same way.
type ParentCascader interface {
	DeleteParent(ctx context.Context, parentID string) (uint32, error)
}

// PrefixCascader is the optional capability to remove every parent whose id
// starts with a prefix as ONE store operation, so a parent created between a
// listing and the deletes cannot survive the cascade.
type PrefixCascader interface {
	DeleteParentsWithPrefix(ctx context.Context, prefix string) (uint32, error)
}

// CascadeParent removes every resource under parentID from s and returns the
// count. It fails closed when s cannot cascade, naming the store, so a caller
// can leave the record that owns parentID in place instead of orphaning what
// s still holds.
func CascadeParent[T Copier[T]](ctx context.Context, s ScopedStore[T], parentID string) (uint32, error) {
	c, ok := s.(ParentCascader)
	if !ok {
		return 0, fmt.Errorf("the store (%T) cannot cascade a parent delete", s)
	}
	return c.DeleteParent(ctx, parentID)
}

// CascadePrefix removes every parent of s whose id starts with prefix and
// returns the count, failing closed like [CascadeParent].
func CascadePrefix[T Copier[T]](ctx context.Context, s ScopedStore[T], prefix string) (uint32, error) {
	c, ok := s.(PrefixCascader)
	if !ok {
		return 0, fmt.Errorf("the store (%T) cannot cascade a prefix delete", s)
	}
	return c.DeleteParentsWithPrefix(ctx, prefix)
}

// ProbeParent checks, before anything is removed, the two things known ahead
// of a [CascadeParent]: that s has the capability, and that a read against it
// (HasParent) succeeds, so a store that is wired but unreachable is refused
// like one that never cascades. It is a read check, not a rehearsal: passing
// does not guarantee the removal succeeds.
func ProbeParent[T Copier[T]](ctx context.Context, s ScopedStore[T], parentID string) error {
	if _, ok := s.(ParentCascader); !ok {
		return fmt.Errorf("the store (%T) cannot cascade a parent delete", s)
	}
	if _, err := s.HasParent(ctx, parentID); err != nil {
		return fmt.Errorf("checking whether the store (%T) holds records under %q: %w", s, parentID, err)
	}
	return nil
}

// ProbePrefix checks that s has the capability [CascadePrefix] requires. It
// reads nothing else: the removal is a single operation, so there is no
// enumeration to rehearse and no parent id to read.
func ProbePrefix[T Copier[T]](_ context.Context, s ScopedStore[T], _ string) error {
	if _, ok := s.(PrefixCascader); !ok {
		return fmt.Errorf("the store (%T) cannot cascade a prefix delete", s)
	}
	return nil
}
