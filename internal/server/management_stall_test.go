package server_test

// #677 fix round item 1: durable-before-live must not cost every reader a
// stalled disk write, only the write itself. Asserted end to end, over a
// real TLS socket while a real admin create is genuinely stalled, per
// section 5a's rule that a claim about blocking rests on a timed request
// over a real socket, not on the shape of the locking code.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/stalltest"
)

// TestStalledManagementWriteDoesNotBlockUnrelatedEndDeviceList is item 1's
// decisive probe. It stalls an admin create's disk write by planting a FIFO
// at the snapshot's <path>.tmp name: atomicfile.Write's
// os.OpenFile(O_WRONLY) on an existing FIFO with no reader blocks at open,
// which gives full control over how long the write stays stalled with no
// dependency on a slow filesystem. While that create is blocked, GET /edev
// from a bystander device (manages nothing, is managed by nobody) must still
// answer promptly: callerDevices (pkg/sep2srv/handlers/enddevice/owner.go)
// calls ManagedBy for every caller of GET /edev, whether or not that caller
// manages anything, so a lock held across the disk write would hold this
// request out too.
func TestStalledManagementWriteDoesNotBlockUnrelatedEndDeviceList(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	c := newSplitListenerCerts(t)
	bystander := newManagementE2EDevice(t, c, "mgmt-stall-bystander")

	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		AdminListen: c.adminProbe,
		AdminKey:    adminTestKey,
		DataDir:     dataDir,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(5 * time.Second):
			t.Error("server.Run did not exit within 5s after cancel")
		}
	})

	if msg, ok := waitForListenersReady(c.sep2Probe, c.adminProbe, bystander.tlsConfig, runErrCh); !ok {
		t.Fatal(msg)
	}
	adminClient := &http.Client{Timeout: 3 * time.Second}

	// Register the bystander so GET /edev has an unambiguous identity to
	// answer for: this device manages nothing and is managed by nobody, so
	// callerDevices calls ManagedBy purely to find that it lists nothing.
	adminMgmtRegisterDevice(t, adminClient, c.adminProbe, bystander.sfdi, bystander.lfdi)

	bystanderClient := ccmHTTPClient(bystander.tlsConfig, stalltest.CallBound)

	// Healthy baseline, before any write ever touches the store: proves the
	// timed comparison below is measuring the stall, not routine handler
	// latency.
	healthyStart := time.Now()
	resp, err := bystanderClient.Get("https://" + c.sep2Probe + "/edev")
	if err != nil {
		t.Fatalf("GET /edev before any stall: %v", err)
	}
	_ = resp.Body.Close()
	healthy := time.Since(healthyStart)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /edev before any stall: status = %d, want 200", resp.StatusCode)
	}

	snapshotPath := filepath.Join(dataDir, "enddevicemanagement.json")
	tmpPath := snapshotPath + ".tmp"
	if err := syscall.Mkfifo(tmpPath, 0o600); err != nil {
		t.Fatalf("Mkfifo(%q): %v", tmpPath, err)
	}
	t.Cleanup(func() { _ = os.Remove(tmpPath) })

	createErrCh := make(chan error, 1)
	go func() {
		body := `{"managerLFDI":"AAAA000000000000000000000000000000000099","managedLFDI":"BBBB000000000000000000000000000000000099"}`
		req, reqErr := http.NewRequest(http.MethodPost, "http://"+c.adminProbe+"/api/management-pairs", strings.NewReader(body))
		if reqErr != nil {
			createErrCh <- reqErr
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+adminTestKey)
		createClient := &http.Client{Timeout: 10 * time.Second}
		createResp, doErr := createClient.Do(req)
		if doErr == nil {
			_ = createResp.Body.Close()
		}
		createErrCh <- doErr
	}()

	// Measure only once the create is parked in atomicfile.Write's open() of
	// the FIFO: a create still on its way would pass the GET vacuously, then
	// block forever on a FIFO whose reader has already come and gone.
	if msg, ok := pollReady("stalled admin create", adminReadyTimeout, runErrCh, goroutineBlockedIn("internal/atomicfile.Write(", "os.OpenFile(")); !ok {
		t.Fatal(msg)
	}

	// #677 fix round item 1: the timing assertions below pass even if the
	// create never reached the FIFO at all (a fast success or failure looks
	// identical to "not yet unblocked" from the bystander's side), and
	// without this check the test then hangs forever at the blocking
	// O_RDONLY open below, since nothing is left to write to the FIFO. A
	// non-blocking receive here turns that into a named failure instead.
	select {
	case doErr := <-createErrCh:
		t.Fatalf("admin create finished before the write could be observed stalled (err=%v): the FIFO at %s was never opened for write, so this run measured nothing", doErr, tmpPath)
	default:
	}

	// The write stays stalled until the reader below opens the FIFO, so a
	// GET that waits on it cannot return inside the bound however loaded
	// the host is.
	stalledStart := time.Now()
	stalltest.Returns(t, "GET /edev while an admin write is stalled", func() {
		resp, err = bystanderClient.Get("https://" + c.sep2Probe + "/edev")
	})
	stalled := time.Since(stalledStart)
	if err != nil {
		t.Fatalf("GET /edev while an admin write is stalled: %v (took %s; healthy request took %s): a reader unrelated to the write must not wait on it", err, stalled, healthy)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /edev while an admin write is stalled: status = %d, want 200", resp.StatusCode)
	}

	// Unblock the stalled write by opening a reader on the FIFO and
	// draining it, then confirm the create eventually completes: this test
	// stops what it started rather than leaving the create request or the
	// server's write goroutine blocked when the test returns. O_NONBLOCK
	// here (#677 fix round item 1) means this open cannot itself hang if the
	// non-blocking check above did not already catch a create that finished
	// early: a nonblocking reader open still completes a blocked writer's
	// pending open, and if no writer is waiting it returns immediately
	// instead of blocking for one that will never arrive.
	reader, err := os.OpenFile(tmpPath, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open FIFO reader to unblock the stalled write: %v", err)
	}
	readerDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, reader)
		close(readerDone)
	}()
	select {
	case doErr := <-createErrCh:
		if doErr != nil {
			t.Logf("create over the FIFO returned an error (expected: a FIFO cannot be fsynced): %v", doErr)
		}
	case <-time.After(5 * time.Second):
		t.Error("create did not complete within 5s of unblocking the FIFO")
	}
	_ = reader.Close()
	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		t.Error("FIFO reader goroutine did not finish within 5s of the writer closing")
	}
}

// goroutineBlockedIn is a pollReady probe that succeeds once some goroutine's
// stack holds every one of frames.
func goroutineBlockedIn(frames ...string) func() error {
	return func() error {
		buf := make([]byte, 1<<20)
		for n := runtime.Stack(buf, true); ; n = runtime.Stack(buf, true) {
			if n < len(buf) {
				buf = buf[:n]
				break
			}
			buf = make([]byte, 2*len(buf))
		}
		for g := range strings.SplitSeq(string(buf), "\n\n") {
			if !slices.ContainsFunc(frames, func(f string) bool { return !strings.Contains(g, f) }) {
				return nil
			}
		}
		return fmt.Errorf("no goroutine is inside all of %q", frames)
	}
}
