package server

import (
	"log"
	"os"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

// newSubscriptionNotifier builds the notification Manager under the
// destination policy cfg selects. The protocol router validates
// notificationURIs through this same Manager, so creation and delivery
// cannot apply different policies.
func newSubscriptionNotifier(cfg *config.Config, subs coresub.SubscriptionLister, workers, queueSize int, opts ...coresub.ManagerOption) *coresub.Manager {
	policy := coresub.DestinationPolicy{AllowLoopback: cfg.NotificationAllowLoopback}
	if policy.AllowLoopback {
		log.Print("WARNING: SEP2_NOTIFICATION_ALLOW_LOOPBACK=true: subscriptions may target loopback addresses, " +
			"including the admin listener; intended for test harnesses only")
	}
	return coresub.NewManager(subs, workers, queueSize, append([]coresub.ManagerOption{coresub.WithDestinationPolicy(policy)}, opts...)...)
}

// notificationTimeoutsFromEnv reads SEP2_NOTIFICATION_POST_TIMEOUT,
// SEP2_NOTIFICATION_DIAL_TIMEOUT and SEP2_NOTIFICATION_RESOLVE_TIMEOUT as Go
// durations such as "45s". Like resolveSubParam, an unset or empty variable
// keeps the default silently, and an unparseable or non-positive one logs a
// warning naming the variable and keeps the default.
func notificationTimeoutsFromEnv() coresub.NotificationTimeouts {
	return coresub.NotificationTimeouts{
		Post:            resolveSubDuration("SEP2_NOTIFICATION_POST_TIMEOUT"),
		Dial:            resolveSubDuration("SEP2_NOTIFICATION_DIAL_TIMEOUT"),
		CreationResolve: resolveSubDuration("SEP2_NOTIFICATION_RESOLVE_TIMEOUT"),
	}
}

// resolveSubDuration returns the positive duration in envKey, or 0 (keep the
// built-in value) when it is unset or invalid.
func resolveSubDuration(envKey string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(envKey))
	if raw == "" {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		log.Printf("WARNING: %s=%q is not a positive duration such as \"30s\"; using the default", envKey, raw)
		return 0
	}
	return d
}
