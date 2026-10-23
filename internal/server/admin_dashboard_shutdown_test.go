package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// An open /dashboard/events stream must not hold the admin server's Shutdown
// to its deadline: Shutdown does not cancel request contexts, so only
// StreamsDone, closed from RegisterOnShutdown as Run wires it, ends it.
func TestAdminShutdownWithOpenDashboardStreamReturnsNil(t *testing.T) {
	streamsDone := make(chan struct{})
	stores := &adminplane.Stores{
		EndDevices:        memory.NewEndDeviceStore(),
		MirrorUsagePoints: memory.NewStore[sep2.MirrorUsagePoint](),
	}
	mux := http.NewServeMux()
	adminplane.NewDashboardHandler(stores, "TLS").WithStreamsDone(streamsDone).RegisterRoutes(mux)

	srv := newAdminServer(mux)
	var once sync.Once
	srv.RegisterOnShutdown(func() { once.Do(func() { close(streamsDone) }) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/dashboard/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "data: ") {
		t.Fatalf("first line = %q, err %v, want a data frame", line, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), adminShutdownTimeout)
	defer cancel()
	start := time.Now()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown = %v, want nil with an open dashboard stream", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("Shutdown took %v, want well under the %v bound", took, adminShutdownTimeout)
	}
}
