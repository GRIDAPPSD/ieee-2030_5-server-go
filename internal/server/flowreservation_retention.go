package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// startRetentionAtBoot is the call Run makes; tests replace it to observe
// when it runs and when its stop is called.
var startRetentionAtBoot = startFlowReservationRetention

// newFlowReservationRetention is the sweep over the stores the routes and
// recovery use. The raw response store is given, not the cascading one: the
// sweep deletes each lifecycle and answer record itself, in its own order.
func newFlowReservationRetention(stores *Stores, notifier flowreservation.Notifier, logger *slog.Logger) *flowreservation.Retention {
	r := &flowreservation.Retention{
		FRQ:        stores.FlowReservationRequests,
		FRP:        stores.FlowReservationResponses,
		Lifecycles: stores.FlowReservationResponseLifecycles,
		Ledger:     stores.CommitmentLedger,
		Fleets:     commitment.Resolver{Devices: stores.EndDevices, Managers: stores.EndDeviceManagers},
		Grace:      stores.FlowReservationRetentionGrace,
		Notifier:   notifier,
		Log:        logger,
	}
	if !store.IsAbsent(stores.FlowReservationAnswers) {
		r.Answers = stores.FlowReservationAnswers
	}
	return r
}

// startFlowReservationRetention sweeps once at now() and then every
// flowreservation.RetentionInterval, and returns the stop for the ticker. A
// failed boot sweep is logged, not fatal: retention is a SHOULD, and the next
// tick retries it. Without the flow reservation stores it does nothing.
func startFlowReservationRetention(ctx context.Context, stores *Stores, notifier flowreservation.Notifier, logger *slog.Logger, now func() time.Time) (stop func()) {
	if store.IsAbsent(stores.FlowReservationRequests) || store.IsAbsent(stores.FlowReservationResponses) ||
		store.IsAbsent(stores.FlowReservationResponseLifecycles) {
		return func() {}
	}
	r := newFlowReservationRetention(stores, notifier, logger)
	if _, err := r.Sweep(ctx, now()); err != nil && ctx.Err() == nil {
		logger.Error("flowreservation: retention: boot sweep failed, retried at the next tick", "err", err)
	}
	return r.Start(ctx, flowreservation.RetentionInterval, now)
}
