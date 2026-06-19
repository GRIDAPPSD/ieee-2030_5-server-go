// CSIP V1.2 §10.2 / §11 — ERR-002: Subscription Survival + Delete-on-400.
//
// ERR-002 asserts the full two-leg error handling contract for the
// subscription subsystem:
//
//  (a) Subscription survives a server power-reset. Uses IEEE-077's
//      SubscriptionRecord snapshot/restore round-trip on the in-process
//      memory store to simulate the restart. After restore, the new
//      Manager delivers Notifications for the surviving subscription.
//
//  (b) Manager deletes a subscription whose receiver returns HTTP 4xx.
//      A 4xx response is per the CSIP V1.2 ERR-002 spec the receiver's
//      signal that the subscription is no longer valid — the server
//      removes it from the store rather than retrying. A 5xx response
//      would be transient and leave the subscription in place; the
//      complementary 5xx-leaves-in-place assertion is pinned by
//      internal/subscription/restart_test.go TestERR002RestartReceiverReturns503,
//      so we don't duplicate that check at the harness level.
//
// V1.2 procedure step → assertion mapping:
//
//	Step 1: build Manager A on Store A, create a subscription whose
//	        NotificationURI points at a NotificationReceiver returning
//	        HTTP 400 (WithStatusCode option).
//	                                          ──► storeA.Create + subscription.NewManager
//	                                          ──► snapshot via SnapshotForTesting
//	Step 2: simulate restart — cancel Manager A, build Store B by
//	        restoring the snapshot, build Manager B on Store B.
//	                                          ──► storeB.RestoreForTesting(snapshot)
//	                                          ──► subscription.NewManager(storeB)
//	                                          ──► sanity: storeB.Get(subID) succeeds
//	Step 3: Notify on Manager B; the receiver gets the Notification
//	        and returns 400. Manager B deletes the subscription from
//	        Store B.
//	                                          ──► mgrB.Notify(href, Changed)
//	                                          ──► receiver.Wait(1)
//	                                          ──► storeB.Get(subID) → ErrNotFound (polled)
//
// No build-tag — ERR-002 exercises the production subscription.Manager
// directly against the in-memory store, no mutation hooks involved.
//
// IEEE-091 / Phase 6. Depends on IEEE-077 (persistence types) + IEEE-080
// (delete-on-400 in Manager) — both cherry-picked onto this branch
// pending #131 + #132 merge.
//
// Note: a unit-level version of the same flow lives in
// internal/subscription/restart_test.go (TestERR002RestartReceiverReturns400).
// This file is the CSIP harness equivalent, exercising the same
// behavior through the csiptest.NotificationReceiver helper (IEEE-087)
// to keep the conformance contract observable from the harness layer
// rather than only the package unit-test layer.

package csip_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/subscription"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-go/test/csip/csiptest"
)

const (
	err002SubscriptionID     = "sub-err002-harness"
	err002SubscribedResource = "/edev/err002/fsa"
)

// TestERR_002_SubscriptionSurvivalAndDeleteOn400 implements CSIP V1.2
// §10.2 / §11 ERR-002.
func TestERR_002_SubscriptionSurvivalAndDeleteOn400(t *testing.T) {
	t.Parallel()

	// Receiver that returns HTTP 400 on every POST. The
	// NotificationReceiver helper (IEEE-087) supports a WithStatusCode
	// option specifically for this kind of fault-injection test.
	receiver := csiptest.NewNotificationReceiver(t, csiptest.WithStatusCode(400))

	// Step 1: Manager A + Store A with the subscription registered.
	storeA := memory.NewSubscriptionStore()
	ctxA, cancelA := context.WithCancel(context.Background())
	sub := sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/err002/sub/1"},
		},
		SubscribedResource: err002SubscribedResource,
		NotificationURI:    receiver.URL(),
		Encoding:           sep2.EncodingXML,
	}
	if err := storeA.Create(ctxA, err002SubscriptionID, sub); err != nil {
		cancelA()
		t.Fatalf("ERR-002 Step 1: Create on Store A: %v", err)
	}

	mgrA := subscription.NewManager(storeA, 2, 16)
	doneA := make(chan struct{})
	go func() {
		mgrA.Start(ctxA)
		close(doneA)
	}()

	snapshot := storeA.SnapshotForTesting()
	if len(snapshot) != 1 || snapshot[0].ID != err002SubscriptionID {
		cancelA()
		t.Fatalf("ERR-002 Step 1: pre-restart snapshot = %+v, want one record with ID %q",
			snapshot, err002SubscriptionID)
	}

	// Step 2: simulate restart. Cancel Manager A, build Store B from
	// the snapshot, attach a fresh Manager B. This is the same
	// restart shape as internal/subscription/restart_test.go.
	cancelA()
	select {
	case <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("ERR-002 Step 2: Manager A did not shut down within 2s")
	}

	storeB := memory.NewSubscriptionStore()
	storeB.RestoreForTesting(snapshot)

	ctxB, cancelB := context.WithCancel(context.Background())
	t.Cleanup(cancelB)
	mgrB := subscription.NewManager(storeB, 2, 16)
	doneB := make(chan struct{})
	go func() {
		mgrB.Start(ctxB)
		close(doneB)
	}()
	t.Cleanup(func() {
		cancelB()
		select {
		case <-doneB:
		case <-time.After(2 * time.Second):
			t.Error("ERR-002: Manager B did not shut down within 2s")
		}
	})

	// Sanity: the subscription survived the restart.
	if _, err := storeB.Get(ctxB, err002SubscriptionID); err != nil {
		t.Fatalf("ERR-002 Step 2: subscription did not survive restart: %v", err)
	}

	// Step 3: Notify on Manager B; receiver returns 400; Manager B
	// deletes the subscription.
	mgrB.Notify(ctxB, err002SubscribedResource, sep2.NotificationStatusChanged)

	// Wait for the receiver to observe the POST (proves the
	// notification was actually delivered before checking the post-
	// delivery store state).
	if _, ok := receiver.Wait(1, 2*time.Second); !ok {
		t.Fatalf("ERR-002 Step 3: receiver never observed the Notification")
	}

	// The Manager dispatches the Delete from the worker goroutine after
	// the receiver's response is read. Poll for the deletion to surface
	// rather than racing against the dispatch.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := storeB.Get(ctxB, err002SubscriptionID)
		if err != nil && errors.Is(err, store.ErrNotFound) {
			break // gone — desired
		}
		if time.Now().After(deadline) {
			t.Fatalf("ERR-002 Step 3: subscription %q still present after receiver 400; want delete-on-4xx",
				err002SubscriptionID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
