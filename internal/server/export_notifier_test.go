package server

import (
	"context"
	"testing"

	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

// NewSubscriptionNotifier exposes the production Manager constructor to the
// external test package.
var NewSubscriptionNotifier = newSubscriptionNotifier

// SetStartNotifier replaces how Run starts the notification manager until t
// ends. Callers must not be parallel.
func SetStartNotifier(t testing.TB, fn func(*coresub.Manager, context.Context)) {
	prev := startNotifier
	startNotifier = fn
	t.Cleanup(func() { startNotifier = prev })
}
