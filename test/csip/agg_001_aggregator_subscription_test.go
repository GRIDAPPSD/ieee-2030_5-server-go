// CSIP V1.2 Section 10.1 - AGG-001 Aggregator Operation Subscription.
//
// Procedure step 2 is one line: "[C] Subscribe to the EndDeviceList."
// The Aggregator posts the subscription to its OWN subscription list
// (CSIP IG 6.2.3.3: "The Aggregator instance contains the
// SubscriptionListLink"), naming the EndDeviceList as the subscribed
// resource. Setup places SubscriptionListLink only on the aggregator's
// EndDevice, never on a managed inverter's.
//
// What this pins down on the server side:
//   - POST /edev/{aggID}/sub accepts a Subscription naming /edev.
//   - The SubscriptionStore preserves SubscribedResource and
//     NotificationURI on the wire.
//   - The aggregator's own subscription list surfaces exactly what was
//     posted, with no extras and no foreign-edev leakage (#168).
//
// Steps 3-5 (server creates EDA1X, sends the notification, client
// receives it and GETs the list) exercise notification *delivery* on a
// topology mutation. That is gated on #12 follow-ups outside #147
// scope: AGG-001 here exercises subscription *acceptance* only, per
// the original test's own scope note.
package csip_test

import (
	"context"
	"testing"
)

// TestAGG_001_AggregatorSubscription implements CSIP V1.2 Section 10.1.
func TestAGG_001_AggregatorSubscription(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	srv := bootAggregatorTopology(t)
	rawClient := srv.HTTPClient()

	// Procedure step 2: subscribe to the EndDeviceList, on the
	// aggregator's own subscription list.
	postAggregatorSubscription(t, ctx, rawClient, srv.BaseURL, aggEDFI, "/edev")

	// The aggregator's own /sub list surfaces exactly that one
	// subscription: no extras, no foreign-edev leakage (#168).
	t.Run("scope_gate", func(t *testing.T) {
		assertAggregatorSubscriptionsPresent(t, ctx, rawClient, srv.BaseURL, aggEDFI, []string{"/edev"})
	})
}
