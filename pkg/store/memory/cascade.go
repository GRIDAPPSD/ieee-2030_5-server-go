package memory

import (
	"context"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// parentCascader is the optional bulk-parent-removal capability
// [ScopedStore.DeleteParent] provides. It is declared here, at the consumer,
// rather than added to store.ScopedStore, for the same reason
// pkg/sep2srv/handlers/metering asserts it locally: the contract addresses
// (parent, id) pairs and offers no key enumeration a generic loop could use,
// so bulk removal is a capability an implementation either has or does not.
type parentCascader interface {
	DeleteParent(ctx context.Context, parentID string) (uint32, error)
}

// deleteScopedParent cascades every resource under parentID out of s and
// fails closed when s cannot cascade, so a caller can leave the record that
// owns parentID in place rather than delete it and orphan whatever s still
// holds (GRIDAPPSD/ieee-2030_5-server-go#701).
func deleteScopedParent[T store.Copier[T]](ctx context.Context, s store.ScopedStore[T], parentID string) error {
	cascader, ok := s.(parentCascader)
	if !ok {
		return fmt.Errorf("the store (%T) cannot cascade a parent delete", s)
	}
	_, err := cascader.DeleteParent(ctx, parentID)
	return err
}

// probeScopedParent reports whether a later deleteScopedParent(ctx, s,
// parentID) is expected to succeed, without removing anything. It checks the
// same capability deleteScopedParent requires, then makes one read call
// (HasParent) so a store that is wired but unreachable is caught the same
// way: [store.ScopedReader.HasParent] must report a failed check as an
// error rather than as false, per its own contract.
//
// This is what lets a decorator chain check every cascade point before any
// of them mutates anything (GRIDAPPSD/ieee-2030_5-server-go#701):
// a probe that fails leaves every store, including this one, untouched.
func probeScopedParent[T store.Copier[T]](ctx context.Context, s store.ScopedStore[T], parentID string) error {
	if _, ok := s.(parentCascader); !ok {
		return fmt.Errorf("the store (%T) cannot cascade a parent delete", s)
	}
	if _, err := s.HasParent(ctx, parentID); err != nil {
		return fmt.Errorf("checking whether the store (%T) holds records under %q: %w", s, parentID, err)
	}
	return nil
}

// deleteProber is implemented by an EndDeviceStore decorator that owns a
// cascade of its own: it can check, without mutating anything, whether its
// Delete(ctx, id) would succeed, and it recurses into whatever it decorates.
// Delete calls probeDelete first and only cascades or deletes when the whole
// chain reports success, so a failure anywhere leaves the whole chain, not
// just the layer that failed: the flow reservation decorator's own cascade
// must not succeed before the LogEvent decorator's cascade fails one layer
// down (GRIDAPPSD/ieee-2030_5-server-go#701).
type deleteProber interface {
	probeDelete(ctx context.Context, id string) error
}

// probeInner runs devs's own probeDelete when it decorates one, so a caller
// need not know whether the layer beneath it has anything to check.
func probeInner(ctx context.Context, devs any, id string) error {
	if inner, ok := devs.(deleteProber); ok {
		return inner.probeDelete(ctx, id)
	}
	return nil
}
