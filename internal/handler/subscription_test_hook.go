//go:build csip_test_hooks

// Build-tag-gated subscription-cancel tombstone for the CSIP V1.2
// conformance harness (MAINT-006). Maintains a small set of canceled
// subscription IDs and an X-CSIP-Test-Subscription-ID header parser so
// the harness can drive the production POST /edev/{id}/sub handler with
// a deterministic ID and assert that a re-create on a canceled ID is
// refused.
//
// Enabled only when the binary is built with `-tags csip_test_hooks`.
// The companion file subscription.go (no tag predicate) declares the
// subscriptionIDOverride and subscriptionRefuseCreate package vars
// defaulted to nil; this file's init() swaps them to closures over the
// canceled-id set. With the tag off this file is not compiled and both
// vars stay nil — the production binary's create path is a single
// nil-compare on each.
//
// IEEE-079.

package handler

import (
	"net/http"
	"sync"
)

// testSubscriptionIDHeader names the request header that lets the
// conformance harness pin a subscription's ID at create time. Case-
// insensitive per net/http. Only honored when the csip_test_hooks tag is
// set; the header is silently ignored in production builds.
const testSubscriptionIDHeader = "X-CSIP-Test-Subscription-ID"

// canceledSubs is the tombstone set. A subscription ID present here is
// refused by HandleCreateSubscription with 409 Conflict.
var (
	canceledSubsMu sync.RWMutex
	canceledSubs   = map[string]struct{}{}
)

func init() {
	subscriptionIDOverride = func(r *http.Request) string {
		return r.Header.Get(testSubscriptionIDHeader)
	}
	subscriptionRefuseCreate = func(id string) bool {
		canceledSubsMu.RLock()
		_, ok := canceledSubs[id]
		canceledSubsMu.RUnlock()
		return ok
	}
}

// MarkSubscriptionCanceled adds id to the tombstone set. Subsequent
// HandleCreateSubscription calls that resolve to the same id return 409
// Conflict. Safe for concurrent use.
//
// TEST-ONLY. Only callable from packages that compile under the
// csip_test_hooks build tag; this symbol does not exist in production
// binaries.
func MarkSubscriptionCanceled(id string) {
	if id == "" {
		return
	}
	canceledSubsMu.Lock()
	canceledSubs[id] = struct{}{}
	canceledSubsMu.Unlock()
}

// IsSubscriptionCanceled reports whether id is in the tombstone set.
// Exposed for assertion helpers in tagged tests.
//
// TEST-ONLY.
func IsSubscriptionCanceled(id string) bool {
	canceledSubsMu.RLock()
	_, ok := canceledSubs[id]
	canceledSubsMu.RUnlock()
	return ok
}

// ResetCanceledSubscriptions clears the tombstone set. Intended for test
// teardown between cases that share process state.
//
// TEST-ONLY.
func ResetCanceledSubscriptions() {
	canceledSubsMu.Lock()
	canceledSubs = map[string]struct{}{}
	canceledSubsMu.Unlock()
}
