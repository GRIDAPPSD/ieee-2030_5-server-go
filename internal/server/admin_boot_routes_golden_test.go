package server_test

import (
	"context"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

var updateAdminBootRoutes = flag.Bool("update-admin-boot-routes", false, "rewrite testdata/admin_boot_routes.golden from a real boot")

const adminBootRoutesGolden = "testdata/admin_boot_routes.golden"

// TestAdminBootRouteListMatchesGolden boots server.Run and compares the admin
// section of the boot route log, line for line, with the golden. Any route
// added to or removed from the admin listener fails it until the golden is
// regenerated with -update-admin-boot-routes and the diff is reviewed.
func TestAdminBootRouteListMatchesGolden(t *testing.T) {
	buf := teeLogOutput(t)

	c := newSplitListenerCerts(t)
	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		AdminListen: c.adminProbe,
		AdminKey:    adminTestKey,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}

	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()

	// The receive on runErrCh orders every log write by Run before the read
	// of buf below.
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case <-runErrCh:
			case <-time.After(5 * time.Second):
				t.Error("server.Run did not exit within 5s after cancel")
			}
		})
	}
	t.Cleanup(stop)

	probe := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(5 * time.Second)
	up := false
	for time.Now().Before(deadline) && !up {
		if resp, err := probe.Get("http://" + c.adminProbe + "/api/certs/ca"); err == nil {
			_ = resp.Body.Close()
			up = true
			break
		}
		select {
		case err := <-runErrCh:
			t.Fatalf("server.Run exited during boot: %v", err)
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !up {
		t.Fatalf("admin listener never became ready on %s", c.adminProbe)
	}
	stop()

	got, ok := adminRouteSection(buf.String(), c.adminProbe)
	if !ok {
		t.Fatalf("no admin route section for %s in the boot log\n---log---\n%s", c.adminProbe, buf.String())
	}

	if *updateAdminBootRoutes {
		if err := os.MkdirAll(filepath.Dir(adminBootRoutesGolden), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(adminBootRoutesGolden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(adminBootRoutesGolden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("admin boot route list differs from %s\n---got---\n%s---want---\n%s", adminBootRoutesGolden, got, want)
	}
}

// adminRouteSection returns the pattern lines logged under the admin listener
// bound at addr, each with its trailing newline. Matching on the address keeps
// another test's boot log in the shared logger from being read instead.
func adminRouteSection(log, addr string) (string, bool) {
	header := "  admin (" + addr + "):\n"
	i := strings.Index(log, header)
	if i < 0 {
		return "", false
	}
	var b strings.Builder
	for _, line := range strings.SplitAfter(log[i+len(header):], "\n") {
		if !strings.HasPrefix(line, "    ") {
			break
		}
		b.WriteString(line)
	}
	return b.String(), true
}
