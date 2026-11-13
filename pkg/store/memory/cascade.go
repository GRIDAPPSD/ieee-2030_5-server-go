package memory

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// deleteScopedParent cascades every resource under parentID out of s and
// drops the count, failing closed through [store.CascadeParent]
// (GRIDAPPSD/ieee-2030_5-server-go#701).
func deleteScopedParent[T store.Copier[T]](ctx context.Context, s store.ScopedStore[T], parentID string) error {
	_, err := store.CascadeParent(ctx, s, parentID)
	return err
}

// deleteScopedParentsUnder removes every parent of s whose id starts with prefix.
func deleteScopedParentsUnder[T store.Copier[T]](ctx context.Context, s store.ScopedStore[T], prefix string) error {
	_, err := store.CascadePrefix(ctx, s, prefix)
	return err
}

// probeResource is [store.ProbeParent] for a flat store.ResourceStore keyed
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
// the delete that follows will succeed; see [store.ProbeParent].
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
