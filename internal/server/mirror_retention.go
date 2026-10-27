package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/mirrorretention"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// newMirrorReadingStore builds the mirror meter reading store. With a data
// directory it is the journal, replayed now, before the retention boot sweep
// and before any listener, with the readings of mirrors that no longer exist
// dropped; the returned journal is for the power history recorder, which
// syncs and compacts it. Without one it is the in-memory store and no journal.
func newMirrorReadingStore(cfg *config.Config, mirrors store.ResourceReader[sep2.MirrorUsagePoint]) (store.ScopedStore[sep2.MirrorMeterReading], *handler.ReadingJournal, error) {
	path := cfg.EffectiveMirrorReadingJournalPath()
	if path == "" {
		return memory.NewScopedStore[sep2.MirrorMeterReading](), nil, nil
	}
	js, err := memory.NewJournaledScopedStore[sep2.MirrorMeterReading](path, "MirrorMeterReading")
	if err != nil {
		return nil, nil, fmt.Errorf("MirrorMeterReading persistence: %w", err)
	}
	j := &handler.ReadingJournal{Readings: js, Mirrors: mirrors}
	if err := j.DropOrphans(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("MirrorMeterReading persistence: %w", err)
	}
	return js, j, nil
}

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
