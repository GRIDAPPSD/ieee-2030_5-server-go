package memory

import (
	"context"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// Test-only snapshot/restore hooks for SubscriptionStore. These exist so the
// CSIP V1.2 ERR-002 conformance harness can simulate a server power-reset
// without persistence work — the harness snapshots the in-memory state,
// rebuilds the server (new Manager + new SubscriptionStore), restores the
// snapshot, and verifies that the restored subscriptions still produce
// notifications.
//
// Path A (this file): test-only hook, no on-disk persistence. Real
// persistence (Path B) is tracked as a separate backlog ticket and will
// replace the need for these hooks for production deployments.
//
// The "ForTesting" suffix mirrors the project's existing idiom (see
// internal/inverter/*_export_test.go for examples) and signals that these
// methods MUST NOT be used outside test code. They are public methods
// (not _export_test.go helpers) because the CSIP conformance harness lives
// in a different package; _export_test.go would hide them from cross-
// package tests.

// SubscriptionRecord pairs a subscription ID with its Subscription value.
// It is the unit of snapshot/restore for SubscriptionStore.
type SubscriptionRecord struct {
	ID           string
	Subscription sep2.Subscription
}

// SnapshotForTesting returns a deep-copied list of every subscription
// currently held by the store, ordered by subscription ID. It is safe to
// call concurrently with other store operations; the returned slice is
// independent of the store's internal state.
//
// TEST-ONLY. Do not call from production code paths. See the package-level
// comment in subscription_testhooks.go for rationale.
func (s *SubscriptionStore) SnapshotForTesting() []SubscriptionRecord {
	s.Store.mu.RLock()
	defer s.Store.mu.RUnlock()

	records := make([]SubscriptionRecord, 0, len(s.Store.keys))
	for _, id := range s.Store.keys {
		sub, ok := s.Store.data[id]
		if !ok {
			continue
		}
		records = append(records, SubscriptionRecord{
			ID:           id,
			Subscription: sub.Copy(),
		})
	}
	return records
}

// RestoreForTesting replaces the store's contents with the supplied
// snapshot, rebuilding all secondary indexes. Any pre-existing state is
// discarded.
//
// TEST-ONLY. Do not call from production code paths.
func (s *SubscriptionStore) RestoreForTesting(snapshot []SubscriptionRecord) {
	// Clear the underlying Store under its write lock.
	s.Store.mu.Lock()
	s.Store.data = make(map[string]sep2.Subscription, len(snapshot))
	s.Store.keys = s.Store.keys[:0]
	s.Store.mu.Unlock()

	// Clear and rebuild secondary indexes under the index lock.
	s.idxMu.Lock()
	s.resourceIndex = make(map[string][]string)
	s.deviceIndex = make(map[string][]string)
	s.idxMu.Unlock()

	// Re-insert via the public Create path so secondary indexes and the
	// sorted key slice stay consistent with normal store invariants. The
	// in-memory store ignores context, so a Background context is fine;
	// duplicate IDs in the snapshot would yield ErrAlreadyExists, which
	// we surface only by skipping the duplicate — first-wins matches the
	// underlying Store's Create contract.
	ctx := context.Background()
	for _, rec := range snapshot {
		_ = s.Create(ctx, rec.ID, rec.Subscription) //nolint:errcheck // see comment above
	}
}
