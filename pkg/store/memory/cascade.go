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
