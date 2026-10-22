package server

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

// deviceLogShutdownTimeout bounds how long Close waits for the queued lines
// to reach disk.
const deviceLogShutdownTimeout = 5 * time.Second

// newDeviceLog starts the device log writer on the directory startup
// resolved and probed. It returns a nil writer and a no-op close when
// logging is off or no directory was resolved, so the router records
// nothing and nothing needs closing.
func newDeviceLog(cfg *config.Config) (*devicelog.Writer, func(), error) {
	if cfg.DeviceLogOff || cfg.DeviceLogResolvedDir == "" {
		return nil, func() {}, nil
	}
	limit, err := cfg.EffectiveDeviceLogMaxBytes()
	if err != nil {
		return nil, nil, fmt.Errorf("SEP2_DEVICE_LOG_MAX_BYTES: %w", err)
	}
	w, err := devicelog.New(devicelog.Config{Dir: cfg.DeviceLogResolvedDir, MaxBytes: limit})
	if err != nil {
		return nil, nil, err
	}
	closeLog := func() {
		ctx, cancel := context.WithTimeout(context.Background(), deviceLogShutdownTimeout)
		defer cancel()
		if cerr := w.Close(ctx); cerr != nil {
			log.Printf("device log close: %s", logsafe.Str(cerr.Error()))
		}
		if n := w.Dropped() + w.Failed(); n > 0 {
			log.Printf("device log: %d lines dropped or lost to file errors since start", n)
		}
		if n := w.Unresolved(); n > 0 {
			log.Printf("device log: %d request lines kept in their device's file because a lookup failed", n)
		}
	}
	return w, closeLog, nil
}
