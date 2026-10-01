package server

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
)

// RecoverFn is the shape of the boot recovery call Run makes.
type RecoverFn = func(context.Context, *Stores, *flowreservation.Queue, flowreservation.Notifier, *slog.Logger, time.Time) (flowreservation.RecoverCounts, error)

// RecoverFlowReservations is the production recovery pass, for a replacement
// to call through.
var RecoverFlowReservations RecoverFn = adminplane.RecoverFlowReservations

// SetRecoverAtBoot replaces the recovery call Run makes until t ends.
// Callers must not be parallel.
func SetRecoverAtBoot(t testing.TB, fn RecoverFn) {
	prev := recoverAtBoot
	recoverAtBoot = fn
	t.Cleanup(func() { recoverAtBoot = prev })
}
