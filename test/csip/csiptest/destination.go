package csiptest

import (
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

// AllowLoopbackReceivers is the destination policy option for a
// subscription.Manager that delivers to NotificationReceiver or any other
// httptest listener, all of which bind 127.0.0.1. The production default
// refuses loopback notificationURIs; BootServer's default Manager uses this.
func AllowLoopbackReceivers() coresub.ManagerOption {
	return coresub.WithDestinationPolicy(coresub.DestinationPolicy{AllowLoopback: true})
}
