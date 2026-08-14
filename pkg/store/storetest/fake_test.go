package storetest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// errBackendDown stands in for a durable backend that has become unreachable:
// a dropped connection, a timeout, a closed pool. It is deliberately not one
// of the store sentinels.
var errBackendDown = errors.New("backend unavailable")

// The fake is a real implementation of the contract, so it is held to the same
// suite the in-memory store is. If the interfaces had been shaped around the
// in-memory store's internals, this is where that would surface.

func TestFakeSatisfiesResourceStoreContract(t *testing.T) {
	t.Parallel()
	storetest.RunResourceStoreSuite(t, func(*testing.T) store.ResourceStore[storetest.Resource] {
		return storetest.NewFake[storetest.Resource]()
	})
}

func TestScopedFakeSatisfiesScopedStoreContract(t *testing.T) {
	t.Parallel()
	storetest.RunScopedStoreSuite(t, func(*testing.T) store.ScopedStore[storetest.Resource] {
		return storetest.NewScopedFake[storetest.Resource]()
	})
}

func TestTransientFailureIsDistinguishableFromEmpty(t *testing.T) {
	t.Parallel()
	storetest.RunTransientFailureSuite(t,
		func(*testing.T) store.ScopedStore[storetest.Resource] {
			return storetest.NewScopedFake[storetest.Resource]()
		},
		func(*testing.T) store.ScopedStore[storetest.Resource] {
			s := storetest.NewScopedFake[storetest.Resource]()
			s.SetFailure(errBackendDown)
			return s
		},
	)
}

// TestFailingScopedLookupDoesNotPresentAsEmpty is the specific failure this
// contract exists to prevent, stated as one test rather than as a suite.
//
// A scoped lookup against an unreachable backend and a scoped lookup against a
// parent that legitimately holds nothing return byte-identical values. If the
// error is dropped, the server answers 200 with an empty list for what is
// really a 500: a silent wrong answer on the wire, and the caller has no way
// to detect it after the fact.
func TestFailingScopedLookupDoesNotPresentAsEmpty(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	healthy := storetest.NewScopedFake[storetest.Resource]()

	down := storetest.NewScopedFake[storetest.Resource]()
	if err := down.Create(ctx, "dev-1", "status", storetest.Resource{ID: "status", Body: "present"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	down.SetFailure(errBackendDown)

	healthyResult, healthyErr := healthy.List(ctx, "dev-1", store.ListOptions{Limit: 10})
	downResult, downErr := down.List(ctx, "dev-1", store.ListOptions{Limit: 10})

	// The value channel cannot tell the two apart. That is the trap.
	if healthyResult.All != downResult.All || healthyResult.Results != downResult.Results {
		t.Fatalf("precondition changed: healthy %+v vs down %+v are no longer value-identical",
			healthyResult, downResult)
	}
	if len(downResult.Items) != 0 {
		t.Fatalf("failing List returned %d items", len(downResult.Items))
	}

	// The error channel must, and it is the only thing that does.
	if healthyErr != nil {
		t.Errorf("healthy empty List err = %v, want nil: empty is not an error", healthyErr)
	}
	if downErr == nil {
		t.Fatal("failing List err = nil: an unreachable backend was reported as an empty collection, " +
			"which becomes 200 with an empty list instead of 500")
	}
	if !errors.Is(downErr, errBackendDown) {
		t.Errorf("failing List err = %v, want it to wrap the backend error", downErr)
	}

	// And it must not be flattened into a sentinel, which would turn a
	// transient failure into a 404 that a client would cache as truth.
	if errors.Is(downErr, store.ErrNotFound) {
		t.Errorf("failing List err = %v, must not be ErrNotFound", downErr)
	}

	// The resource is still there. The store did not lose it; it could not be
	// reached. Once the backend recovers the correct answer returns, which is
	// exactly why the empty answer was wrong rather than merely stale.
	down.SetFailure(nil)
	recovered, err := down.List(ctx, "dev-1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("List after recovery: %v", err)
	}
	if recovered.Results != 1 || len(recovered.Items) != 1 || recovered.Items[0].Body != "present" {
		t.Errorf("after recovery = %+v, want the one seeded resource", recovered)
	}
}
