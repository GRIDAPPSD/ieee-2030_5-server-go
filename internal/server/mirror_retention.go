package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mirrorretention"
)

// startMirrorRetentionAtBoot is the call Run makes; tests replace it to
// observe when it runs and when its stop is called.
var startMirrorRetentionAtBoot = startMirrorReadingRetention

// startMirrorReadingRetention sweeps the mirror reading store once at now()
// and then every mirrorretention.Interval, and returns the stop for the
// ticker. A failed boot sweep is logged, not fatal: the next tick retries it.
// It takes no fleet lock, since no commitment guards a reading.
func startMirrorReadingRetention(ctx context.Context, stores *Stores, logger *slog.Logger, now func() time.Time) (stop func()) {
	if stores.MirrorMeterReadings == nil {
		return func() {}
	}
	r := &mirrorretention.Retention{
		Readings:     stores.MirrorMeterReadings,
		MaxAge:       stores.MirrorReadingRetention,
		MaxPerMirror: stores.MirrorReadingMaxPerMirror,
		Log:          logger,
	}
	if _, err := r.Sweep(ctx, now()); err != nil && ctx.Err() == nil {
		logger.Error("mirrorretention: boot sweep failed, retried at the next tick", "err", err)
	}
	return r.Start(ctx, mirrorretention.Interval, now)
}
