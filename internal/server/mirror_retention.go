package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mirrorretention"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// startMirrorRetentionAtBoot is the call Run makes; tests replace it to
// observe when it runs and when its stop is called.
var startMirrorRetentionAtBoot = startMirrorReadingRetention

// startMirrorReadingRetention sweeps the mirror reading store once at now()
// and then every mirrorretention.Interval, and returns the stop for the
// ticker. A failed boot sweep is logged, not fatal: the next tick retries it.
// It takes no fleet lock, since no commitment guards a reading.
func startMirrorReadingRetention(ctx context.Context, stores *Stores, logger *slog.Logger, now func() time.Time) (stop func()) {
	if store.IsAbsent(stores.MirrorMeterReadings) {
		return func() {}
	}
	r := &mirrorretention.Retention{
		Readings:     stores.MirrorMeterReadings,
		MaxAge:       stores.MirrorReadingRetention,
		MaxPerSeries: stores.MirrorReadingMaxPerSeries,
		Log:          logger,
	}
	// A nil mirror store leaves Mirrors nil, so every sweep refuses rather
	// than removing a type an inline reading relies on.
	if !store.IsAbsent(stores.MirrorUsagePoints) {
		r.Mirrors = stores.MirrorUsagePoints
	}
	if _, err := r.Sweep(ctx, now()); err != nil && ctx.Err() == nil {
		logger.Error("mirrorretention: boot sweep failed, retried at the next tick", "err", err)
	}
	return r.Start(ctx, mirrorretention.Interval, now)
}
