package server

import (
	"log"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

// newSubscriptionNotifier builds the notification Manager under the
// destination policy cfg selects. The protocol router validates
// notificationURIs through this same Manager, so creation and delivery
// cannot apply different policies.
func newSubscriptionNotifier(cfg *config.Config, subs coresub.SubscriptionLister, workers, queueSize int) *coresub.Manager {
	policy := coresub.DestinationPolicy{AllowLoopback: cfg.NotificationAllowLoopback}
	if policy.AllowLoopback {
		log.Print("WARNING: SEP2_NOTIFICATION_ALLOW_LOOPBACK=true: subscriptions may target loopback addresses, " +
			"including the admin listener; intended for test harnesses only")
	}
	return coresub.NewManager(subs, workers, queueSize, coresub.WithDestinationPolicy(policy))
}
