package handler

import (
	"context"

	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
)

// SubscriberNotifier sends a single "Removed" Notification to one
// subscriber. Defined here at the consumer (Pike rule: interfaces at the
// consumer) so the handler can take nil from tests that don't exercise
// the notification pipeline. Production wiring passes
// *subscription.Manager.
//
// IEEE-100 / CSIP V1.2 §11.6.
type SubscriberNotifier interface {
	NotifyRemoved(ctx context.Context, sub sep2.Subscription) error
}
