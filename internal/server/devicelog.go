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

// startDeviceLog is newDeviceLog behind a variable so a test can see the
// writer Run starts and check that Run closed it.
var startDeviceLog DeviceLogStarter = newDeviceLog

// DeviceLogStarter is the type of newDeviceLog.
type DeviceLogStarter = func(cfg *config.Config) (*devicelog.Writer, func(), error)

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
		if n := w.Unsettled(); n > 0 {
			log.Printf("device log: %d request lines were written by the fallback rule (their device file if it already existed, else the shared file, no target) because a lookup failed or had not settled in time", n)
		}
		for _, st := range w.Stats() {
			if n := st.Value(); n > 0 {
				log.Printf("device log: %s: %d", logsafe.Str(st.Name), n)
			}
		}
	}
	return w, closeLog, nil
}

// newDeviceLogReader is the admin plane's reader over the directory the
// writer w appends to. It is nil when logging is off (w is nil), which the
// read routes answer with 503. A directory that cannot be read as a plain
// directory is an error: the writer started on it, so this is not expected.
func newDeviceLogReader(cfg *config.Config, w *devicelog.Writer) (*devicelog.Reader, error) {
	if w == nil {
		return nil, nil
	}
	r, err := devicelog.NewReader(cfg.DeviceLogResolvedDir)
	if err != nil {
		return nil, err
	}
	return r.WithDropped(func() uint64 { return w.Dropped() + w.Failed() }), nil
}
