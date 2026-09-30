package memory

import (
	"context"
	"errors"
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

// probeScopedParent checks the two things known ahead of the actual cascade:
// that s HAS the capability deleteScopedParent requires, and that a read
// against it (HasParent) succeeds. [store.ScopedReader.HasParent] must
// report a failed check as an error rather than as false, per its own
// contract, so this catches a store that is wired but currently unreachable
// the same way it catches one that never supports DeleteParent at all.
//
// This is a READ check, not a rehearsal of the delete itself: passing it
// means the cascade is not already known to fail, not that DeleteParent is
// guaranteed to succeed. A store can pass this probe and still fail the
// actual DeleteParent call, for reasons the probe cannot see (the capability
// and reachability it checks are not the same call as the removal). What it
// closes is the case found in GRIDAPPSD/ieee-2030_5-server-go#701: a layer
// that CANNOT cascade, or is already unreachable, is refused before any
// layer in the chain removes anything, rather than discovered only after an
// earlier layer's cascade has already mutated its store.
func probeScopedParent[T store.Copier[T]](ctx context.Context, s store.ScopedStore[T], parentID string) error {
	if _, ok := s.(parentCascader); !ok {
		return fmt.Errorf("the store (%T) cannot cascade a parent delete", s)
	}
	if _, err := s.HasParent(ctx, parentID); err != nil {
		return fmt.Errorf("checking whether the store (%T) holds records under %q: %w", s, parentID, err)
	}
	return nil
}

// probeResource is [probeScopedParent] for a flat store.ResourceStore keyed
// directly by id, such as Registrations: Delete on that shape is always
// present (there is no optional capability to check), so the only thing
// worth checking ahead of time is reachability, via Get. ErrNotFound is not
// a failure here: a later Delete tolerates it the same way, since an absent
// record needs no removal.
func probeResource[T store.Copier[T]](ctx context.Context, s store.ResourceStore[T], id string) error {
	if _, err := s.Get(ctx, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("checking whether the store (%T) can be read for %q: %w", s, id, err)
	}
	return nil
}

// deleteProber is implemented by an EndDeviceStore decorator that owns a
// cascade of its own: it can check, via a read, whether its Delete(ctx, id)
// looks likely to succeed, without mutating anything, and it recurses into
// whatever it decorates. Delete calls probeDelete first and only cascades or
// deletes when the whole chain's probe passes, so a layer that already
// cannot cascade, found anywhere in the chain, is refused before any layer
// removes anything: the flow reservation decorator's own cascade must not
// run ahead of the LogEvent decorator's cascade being refused one layer down
// (GRIDAPPSD/ieee-2030_5-server-go#701). A probe passing is not a guarantee
// the delete that follows will succeed; see [probeScopedParent].
type deleteProber interface {
	probeDelete(ctx context.Context, id string) error
}

// probeInner runs devs's own probeDelete when it decorates one, so a caller
// need not know whether the layer beneath it has anything to check.
func probeInner(ctx context.Context, devs store.EndDeviceStore, id string) error {
	if inner, ok := devs.(deleteProber); ok {
		return inner.probeDelete(ctx, id)
	}
	return nil
}
