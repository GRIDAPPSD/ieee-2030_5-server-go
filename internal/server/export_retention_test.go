package server

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
)

// NewFlowReservationRetention is the sweep Run builds, over stores.
var NewFlowReservationRetention = adminplane.NewFlowReservationRetention

// StartRetentionFn is the shape of the retention start Run makes.
type StartRetentionFn = func(context.Context, *Stores, flowreservation.Notifier, *slog.Logger, func() time.Time) func()

// StartFlowReservationRetention is the production start, for a replacement
// to call through.
var StartFlowReservationRetention StartRetentionFn = adminplane.StartFlowReservationRetention

// SetStartRetentionAtBoot replaces the retention start Run makes until t
// ends. Callers must not be parallel.
func SetStartRetentionAtBoot(t testing.TB, fn StartRetentionFn) {
	prev := startRetentionAtBoot
	startRetentionAtBoot = fn
	t.Cleanup(func() { startRetentionAtBoot = prev })
}

// StartMirrorRetentionFn is the shape of the mirror reading retention start
// Run makes.
type StartMirrorRetentionFn = func(context.Context, *Stores, *slog.Logger, func() time.Time) func()

// StartMirrorReadingRetention is the production start, for a replacement to
// call through.
var StartMirrorReadingRetention StartMirrorRetentionFn = startMirrorReadingRetention

// SetStartMirrorRetentionAtBoot replaces the mirror reading retention start
// Run makes until t ends. Callers must not be parallel.
func SetStartMirrorRetentionAtBoot(t testing.TB, fn StartMirrorRetentionFn) {
	prev := startMirrorRetentionAtBoot
	startMirrorRetentionAtBoot = fn
	t.Cleanup(func() { startMirrorRetentionAtBoot = prev })
}
