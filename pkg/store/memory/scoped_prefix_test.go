package memory_test

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

type prefixDeleter interface {
	store.ScopedStore[storetest.Resource]
	DeleteParentsWithPrefix(ctx context.Context, prefix string) (uint32, error)
}

func runPrefixDelete(t *testing.T, s prefixDeleter) {
	t.Helper()
	ctx := context.Background()
	for _, p := range []string{"e1/1", "e1/2", "e10/1", "e2/1"} {
		if err := s.Create(ctx, p, "k", storetest.Resource{ID: "k", Body: p}); err != nil {
			t.Fatalf("seed %s: %v", p, err)
		}
	}
	n, err := s.DeleteParentsWithPrefix(ctx, "e1/")
	if err != nil || n != 2 {
		t.Fatalf("DeleteParentsWithPrefix(e1/) = %d, %v, want 2, nil", n, err)
	}
	for _, p := range []string{"e1/1", "e1/2"} {
		if c, _ := s.Count(ctx, p); c != 0 {
			t.Errorf("%s holds %d records after the prefix delete", p, c)
		}
	}
	for _, p := range []string{"e10/1", "e2/1"} {
		if c, _ := s.Count(ctx, p); c != 1 {
			t.Errorf("%s holds %d records, want 1: the prefix delete reached past its prefix", p, c)
		}
	}
	if _, err := s.DeleteParentsWithPrefix(ctx, ""); err == nil {
		t.Error("an empty prefix was accepted; it would remove every parent")
	}
}

func TestScopedStore_DeleteParentsWithPrefix(t *testing.T) {
	t.Parallel()
	runPrefixDelete(t, memory.NewScopedStore[storetest.Resource]())
}

func TestPersistentScopedStore_DeleteParentsWithPrefix_PersistsAcrossReload(t *testing.T) {
	t.Parallel()
	s, path := newPersistentResources(t)
	runPrefixDelete(t, s)

	reloaded, err := memory.NewPersistentScopedStore[storetest.Resource](path, "resources")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	ctx := context.Background()
	if c, _ := reloaded.Count(ctx, "e1/1"); c != 0 {
		t.Errorf("e1/1 came back after reload with %d records", c)
	}
	if c, _ := reloaded.Count(ctx, "e10/1"); c != 1 {
		t.Errorf("e10/1 holds %d records after reload, want 1", c)
	}
}

type parentsCounting struct {
	*memory.ScopedStore[sep2.DERStatus]
	parentsCalls int
}

func (s *parentsCounting) Parents(ctx context.Context) ([]string, error) {
	s.parentsCalls++
	return s.ScopedStore.Parents(ctx)
}

// The device delete removes a device's composite parents in one store
// operation: enumerating the parents first (and again to probe) is the window
// in which a parent created in between survives the cascade.
func TestDeviceKeyedCascade_DeleteDoesNotEnumerateParents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	statuses := &parentsCounting{ScopedStore: memory.NewScopedStore[sep2.DERStatus]()}
	if err := statuses.Create(ctx, "e1/1", "default", sep2.DERStatus{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	devs := memory.NewEndDeviceStore()
	if err := devs.Create(ctx, "e1", sep2.EndDevice{}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	s := memory.NewDeviceKeyedCascadeEndDeviceStore(devs, nil, nil, nil, nil, nil).
		WithDERs(nil, nil, nil, statuses, nil)

	if err := s.Delete(ctx, "e1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n, _ := statuses.Count(ctx, "e1/1"); n != 0 {
		t.Errorf("DERStatus under e1/1 = %d after the delete, want 0", n)
	}
	if statuses.parentsCalls != 0 {
		t.Errorf("Parents was called %d times during one device delete, want 0", statuses.parentsCalls)
	}
}
