package storetest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// TestErrBackendUnavailableIsNotASentinel pins the property the whole fault
// probe rests on: the injected error matches none of the four errors that carry
// defined meaning.
//
// If it ever did match one, a route that renders it as a 404 or an empty list
// would be CORRECT to do so, and every assertion built on this probe would be
// testing nothing.
func TestErrBackendUnavailableIsNotASentinel(t *testing.T) {
	t.Parallel()

	for _, sentinel := range []struct {
		name string
		err  error
	}{
		{"ErrNotFound", store.ErrNotFound},
		{"ErrAlreadyExists", store.ErrAlreadyExists},
		{"ErrUnsupportedSort", store.ErrUnsupportedSort},
		{"ErrInvalidListOptions", store.ErrInvalidListOptions},
		{"ErrInvalidManagementPair", store.ErrInvalidManagementPair},
	} {
		if errors.Is(storetest.ErrBackendUnavailable, sentinel.err) {
			t.Errorf("ErrBackendUnavailable matches store.%s; a transient failure must match no sentinel", sentinel.name)
		}
	}
}

// TestFaultArmingDrivesEveryMethod asserts every method of both decorators
// returns the armed error, and returns the wrapped store's own answer once the
// switch is disarmed.
//
// Every method is listed rather than a representative few: a method that
// forgot the fault check is exactly the hole that would make a route look
// covered while never failing at all.
func TestFaultArmingDrivesEveryMethod(t *testing.T) {
	t.Parallel()

	var fault storetest.Fault
	flat := storetest.NewFaultyResourceStore[storetest.Resource](storetest.NewFake[storetest.Resource](), &fault)
	scoped := storetest.NewFaultyScopedStore[storetest.Resource](storetest.NewScopedFake[storetest.Resource](), &fault)

	ctx := context.Background()
	value := storetest.Resource{ID: "a", Body: "b"}

	flatOps := map[string]func() error{
		"Get":    func() error { _, err := flat.Get(ctx, "a"); return err },
		"List":   func() error { _, err := flat.List(ctx, store.ListOptions{Limit: 10}); return err },
		"Count":  func() error { _, err := flat.Count(ctx); return err },
		"Create": func() error { return flat.Create(ctx, "a", value) },
		"Update": func() error { return flat.Update(ctx, "a", value) },
		"Delete": func() error { return flat.Delete(ctx, "a") },
	}
	scopedOps := map[string]func() error{
		"Get":       func() error { _, err := scoped.Get(ctx, "p", "a"); return err },
		"List":      func() error { _, err := scoped.List(ctx, "p", store.ListOptions{Limit: 10}); return err },
		"Count":     func() error { _, err := scoped.Count(ctx, "p"); return err },
		"HasParent": func() error { _, err := scoped.HasParent(ctx, "p"); return err },
		"Parents":   func() error { _, err := scoped.Parents(ctx); return err },
		"Create":    func() error { return scoped.Create(ctx, "p", "a", value) },
		"Update":    func() error { return scoped.Update(ctx, "p", "a", value) },
		"Delete":    func() error { return scoped.Delete(ctx, "p", "a") },
	}

	fault.Arm(storetest.ErrBackendUnavailable)
	for name, op := range flatOps {
		if err := op(); !errors.Is(err, storetest.ErrBackendUnavailable) {
			t.Errorf("armed flat %s returned %v, want ErrBackendUnavailable", name, err)
		}
	}
	for name, op := range scopedOps {
		if err := op(); !errors.Is(err, storetest.ErrBackendUnavailable) {
			t.Errorf("armed scoped %s returned %v, want ErrBackendUnavailable", name, err)
		}
	}

	// Disarmed, the decorator is transparent: a Create succeeds and the Get
	// that follows it returns the stored value rather than the fault.
	fault.Disarm()
	if err := flat.Create(ctx, "a", value); err != nil {
		t.Fatalf("disarmed flat Create: %v", err)
	}
	got, err := flat.Get(ctx, "a")
	if err != nil {
		t.Fatalf("disarmed flat Get: %v", err)
	}
	if got.Body != "b" {
		t.Errorf("disarmed flat Get body = %q, want %q: the decorator altered the value it forwards", got.Body, "b")
	}
	if err := scoped.Create(ctx, "p", "a", value); err != nil {
		t.Fatalf("disarmed scoped Create: %v", err)
	}
	if _, err := scoped.Get(ctx, "p", "a"); err != nil {
		t.Fatalf("disarmed scoped Get: %v", err)
	}

	// A disarmed decorator over an empty parent still reports the store's own
	// answer, which is the empty-versus-failed distinction the contract is
	// about: the same zero-valued result, and this time with a nil error.
	res, err := scoped.List(ctx, "unknown-parent", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("disarmed scoped List of an unknown parent: %v", err)
	}
	if res.All != 0 || len(res.Items) != 0 {
		t.Errorf("disarmed List of an unknown parent = all %d, %d items; want an empty result", res.All, len(res.Items))
	}
}

// cascadingScoped is a ScopedStore that also provides the optional parent
// cascade, standing in for the in-memory store's DeleteParent.
type cascadingScoped struct {
	*storetest.ScopedFake[storetest.Resource]
	calls int
}

func (s *cascadingScoped) DeleteParent(_ context.Context, _ string) (uint32, error) {
	s.calls++
	return 3, nil
}

// TestFaultyScopedStorePreservesTheCascadeCapability asserts the decorator
// claims DeleteParent exactly when the store it wraps does.
//
// Both directions matter, and both are failure modes a consumer would see as a
// wrong status code with no visible cause. A decorator that DROPPED the
// capability would push DELETE /mup/{id} down the metering handler's
// cannot-cascade refusal, which answers 500 whatever the fault switch is doing,
// so a route test would pass while measuring the wrapper. A decorator that
// INVENTED it would let that handler complete a parent delete whose children
// nothing removed, which is the orphaning the refusal exists to prevent.
func TestFaultyScopedStorePreservesTheCascadeCapability(t *testing.T) {
	t.Parallel()

	type cascader interface {
		DeleteParent(ctx context.Context, parentID string) (uint32, error)
	}

	var fault storetest.Fault

	plain := storetest.NewFaultyScopedStore[storetest.Resource](storetest.NewScopedFake[storetest.Resource](), &fault)
	if _, ok := plain.(cascader); ok {
		t.Error("the decorator claims DeleteParent over a store that does not provide it")
	}

	inner := &cascadingScoped{ScopedFake: storetest.NewScopedFake[storetest.Resource]()}
	wrapped := storetest.NewFaultyScopedStore[storetest.Resource](inner, &fault)
	forwarding, ok := wrapped.(cascader)
	if !ok {
		t.Fatal("the decorator dropped DeleteParent from a store that provides it")
	}

	removed, err := forwarding.DeleteParent(context.Background(), "p")
	if err != nil {
		t.Fatalf("disarmed DeleteParent: %v", err)
	}
	if removed != 3 || inner.calls != 1 {
		t.Errorf("DeleteParent returned %d after %d inner calls, want 3 after 1: the decorator did not forward", removed, inner.calls)
	}

	fault.Arm(storetest.ErrBackendUnavailable)
	if _, err := forwarding.DeleteParent(context.Background(), "p"); !errors.Is(err, storetest.ErrBackendUnavailable) {
		t.Errorf("armed DeleteParent returned %v, want ErrBackendUnavailable", err)
	}
	if inner.calls != 1 {
		t.Errorf("armed DeleteParent reached the inner store (%d calls, want 1)", inner.calls)
	}
}

// TestFaultyStoresPassTheContractSuiteWhileDisarmed runs the contract's own
// conformance suite against the decorators, so "the decorator is transparent"
// is a checked claim rather than an assertion about six forwarding methods.
func TestFaultyStoresPassTheContractSuiteWhileDisarmed(t *testing.T) {
	t.Parallel()

	storetest.RunResourceStoreSuite(t, func(t *testing.T) store.ResourceStore[storetest.Resource] {
		return storetest.NewFaultyResourceStore[storetest.Resource](storetest.NewFake[storetest.Resource](), &storetest.Fault{})
	})
	storetest.RunScopedStoreSuite(t, func(t *testing.T) store.ScopedStore[storetest.Resource] {
		return storetest.NewFaultyScopedStore[storetest.Resource](storetest.NewScopedFake[storetest.Resource](), &storetest.Fault{})
	})
	storetest.RunTransientFailureSuite(t,
		func(t *testing.T) store.ScopedStore[storetest.Resource] {
			return storetest.NewFaultyScopedStore[storetest.Resource](storetest.NewScopedFake[storetest.Resource](), &storetest.Fault{})
		},
		func(t *testing.T) store.ScopedStore[storetest.Resource] {
			fault := &storetest.Fault{}
			fault.Arm(storetest.ErrBackendUnavailable)
			return storetest.NewFaultyScopedStore[storetest.Resource](storetest.NewScopedFake[storetest.Resource](), fault)
		},
	)
}
