package server_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #806: Run starts the mirror reading sweep with the configured bounds, the
// boot sweep removes an aged reading from the store the routes write, and the
// sweep is stopped when Run returns.
func TestRun_SweepsMirrorReadingsAtBoot(t *testing.T) {
	e := newFRRunEnv(t)
	cfg := e.config(0)
	cfg.MirrorReadingRetention = 87300 * time.Second
	cfg.MirrorReadingMaxPerSeries = 300

	var (
		mu                 sync.Mutex
		maxAge             time.Duration
		maxPer, left       int
		started, stopped   bool
		agedID, youngID    string
		seedErr, listErr   error
		readingsAfterSweep []sep2.MirrorMeterReading
	)
	server.SetStartMirrorRetentionAtBoot(t, func(ctx context.Context, s *server.Stores, l *slog.Logger, now func() time.Time) func() {
		mu.Lock()
		defer mu.Unlock()
		started, maxAge, maxPer = true, s.MirrorReadingRetention, s.MirrorReadingMaxPerSeries
		at := now()
		for i, age := range []time.Duration{87301 * time.Second, time.Hour} {
			nanos := at.Add(-age).UnixNano() + int64(i)
			id := fmt.Sprintf("%020d", nanos)
			m := sep2.MirrorMeterReading{MRID: "W", LastUpdateTime: time.Unix(0, nanos).Unix()}
			m.Href = "/mup/m1/mr/" + id
			if err := s.MirrorMeterReadings.Create(ctx, "m1", id, m); err != nil {
				seedErr = err
			}
			if i == 0 {
				agedID = id
			} else {
				youngID = id
			}
		}
		stop := server.StartMirrorReadingRetention(ctx, s, l, now)
		page, err := s.MirrorMeterReadings.List(ctx, "m1", store.ListOptions{Unbounded: true})
		readingsAfterSweep, listErr = page.Items, err
		return func() {
			stop()
			mu.Lock()
			defer mu.Unlock()
			stopped = true
			left++
		}
	})

	stop := e.start(t, cfg)
	if err := stop(); err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !started || !stopped || left != 1 {
		t.Fatalf("started %v, stopped %v (%d), want one start and one stop", started, stopped, left)
	}
	if maxAge != 87300*time.Second || maxPer != 300 {
		t.Errorf("stores carry retention %v and cap %d, want 24h15m0s and 300", maxAge, maxPer)
	}
	if seedErr != nil || listErr != nil {
		t.Fatalf("seed %v, list %v", seedErr, listErr)
	}
	if len(readingsAfterSweep) != 1 || readingsAfterSweep[0].Href != "/mup/m1/mr/"+youngID {
		t.Fatalf("after the boot sweep = %+v, want only %s (aged %s removed)", readingsAfterSweep, youngID, agedID)
	}
}
