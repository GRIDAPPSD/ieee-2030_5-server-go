package server

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
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

// Not parallel: it redirects the standard logger.
func TestDeviceLogShutdownNamesBothCounters(t *testing.T) {
	var out bytes.Buffer
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	w, closeLog, err := newDeviceLog(&config.Config{DeviceLogResolvedDir: t.TempDir()})
	if err != nil || w == nil {
		t.Fatalf("newDeviceLog = %v, %v", w, err)
	}
	w.AddStat("lookups refused at the in-flight limit", func() uint64 { return 3 })
	const lfdi = "0123456789ABCDEF0123456789ABCDEF01234567"
	stillPending := func(_, _ string) devicelog.Resolution { return devicelog.Resolution{Pending: true} }
	if err := w.AppendResolved(lfdi, "", stillPending, devicelog.Entry{Kind: "request"}); err != nil {
		t.Fatal(err)
	}
	closeLog() // releases the parked line by the fallback, which counts it

	for _, want := range []string{
		"1 request lines were written by the fallback rule",
		"lookups refused at the in-flight limit: 3",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("shutdown output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestNewDeviceLogReaderIsNilWhenLoggingIsOff(t *testing.T) {
	t.Parallel()
	r, err := newDeviceLogReader(&config.Config{DeviceLogOff: true}, nil)
	if err != nil || r != nil {
		t.Errorf("newDeviceLogReader with no writer = %v, %v; want nil, nil", r, err)
	}
}

func TestNewDeviceLogReaderReportsTheWritersDroppedLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := &config.Config{DeviceLogResolvedDir: dir}
	w, closeLog, err := newDeviceLog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const lfdi = "0123456789ABCDEF0123456789ABCDEF01234567"
	if err := w.Append(lfdi, devicelog.Entry{Kind: "request"}); err != nil {
		t.Fatal(err)
	}
	closeLog()
	// The writer is closed, so this line is dropped and counted.
	if err := w.Append(lfdi, devicelog.Entry{Kind: "request"}); err == nil {
		t.Fatal("Append after close succeeded")
	}
	r, err := newDeviceLogReader(cfg, w)
	if err != nil || r == nil {
		t.Fatalf("newDeviceLogReader = %v, %v; want a reader", r, err)
	}
	page, err := r.Read(context.Background(), devicelog.Query{Sources: []devicelog.Source{{File: lfdi}}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 1 || page.Dropped != 1 {
		t.Errorf("page has %d entries and dropped=%d; want 1 entry from the file and dropped=1 from the writer", len(page.Entries), page.Dropped)
	}
}
