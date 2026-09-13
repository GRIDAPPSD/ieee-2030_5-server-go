package memory_test

import (
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// The in-memory store is held to the same conformance suite as every other
// implementation of the contract. The ordering assertions in particular turn
// what used to be an emergent property of the sorted key slice into a stated
// guarantee: IEEE 2030.5 section 4.6.1 requires a defined list order, and the
// paging parameters address positions within it.

func TestMemoryStoreSatisfiesResourceStoreContract(t *testing.T) {
	t.Parallel()
	storetest.RunResourceStoreSuite(t, func(*testing.T) store.ResourceStore[storetest.Resource] {
		return memory.NewStore[storetest.Resource]()
	})
}

func TestMemoryScopedStoreSatisfiesScopedStoreContract(t *testing.T) {
	t.Parallel()
	storetest.RunScopedStoreSuite(t, func(*testing.T) store.ScopedStore[storetest.Resource] {
		return memory.NewScopedStore[storetest.Resource]()
	})
}

func TestMemoryEndDeviceManagementStoreSatisfiesContract(t *testing.T) {
	t.Parallel()
	storetest.RunEndDeviceManagementSuite(t, func(*testing.T) store.EndDeviceManagementStore {
		return memory.NewEndDeviceManagementStore()
	})
}
