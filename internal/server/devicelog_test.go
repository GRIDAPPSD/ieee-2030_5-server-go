package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

func TestNewDeviceLogOffOrUnresolvedReturnsNil(t *testing.T) {
	t.Parallel()
	for name, cfg := range map[string]*config.Config{
		"off":        {DeviceLogOff: true, DeviceLogResolvedDir: t.TempDir()},
		"unresolved": {},
	} {
		w, closeLog, err := newDeviceLog(cfg)
		if err != nil || w != nil || closeLog == nil {
			t.Errorf("%s: writer=%v close nil=%v err=%v; want nil writer, a close func, no error", name, w, closeLog == nil, err)
			continue
		}
		closeLog()
	}
}

func TestNewDeviceLogWritesToResolvedDirAndClosesDrained(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	w, closeLog, err := newDeviceLog(&config.Config{DeviceLogResolvedDir: dir})
	if err != nil || w == nil {
		t.Fatalf("newDeviceLog = %v, %v; want a writer", w, err)
	}
	const lfdi = "0123456789ABCDEF0123456789ABCDEF01234567"
	if err := w.Append(lfdi, devicelog.Entry{Kind: "request"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	closeLog()
	b, err := os.ReadFile(filepath.Join(dir, lfdi+".jsonl"))
	if err != nil || len(b) == 0 {
		t.Errorf("after close the queued line must be on disk: %v, %d bytes", err, len(b))
	}
}

func TestNewDeviceLogRefusesOutOfRangeLimit(t *testing.T) {
	t.Parallel()
	if _, _, err := newDeviceLog(&config.Config{DeviceLogResolvedDir: t.TempDir(), DeviceLogMaxBytes: 5}); err == nil {
		t.Error("limit 5 accepted; want a refusal")
	}
}
