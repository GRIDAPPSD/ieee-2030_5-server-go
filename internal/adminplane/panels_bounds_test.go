package adminplane

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

// routerFor builds the plane around panels with a short View deadline.
func routerFor(t *testing.T, timeout time.Duration, panels ...sep2admin.Panel) http.Handler {
	t.Helper()
	ps, err := newPanelSet(panels)
	if err != nil {
		t.Fatalf("newPanelSet: %v", err)
	}
	ps.timeout = timeout
	cfg := runConfig(panelTestKey, nil, nil, "GCM", nil, nil, false, nil)
	authed, withMW := buildAuthedAdminMux(cfg, ps)
	h, _ := buildOuterAdminRouter(cfg, authed, withMW)
	return h
}

// goroutineGrowth reads 40 times and reports how many goroutines remain
// above the starting count once the count settles.
func goroutineGrowth(t *testing.T, h http.Handler, path string, wantStatus int) int {
	t.Helper()
	before := runtime.NumGoroutine()
	for i := range 40 {
		if rec := get(h, path, true); rec.Code != wantStatus {
			t.Fatalf("read %d: status %d, want %d; body %s", i, rec.Code, wantStatus, rec.Body)
		}
	}
	growth := runtime.NumGoroutine() - before
	for deadline := time.Now().Add(2 * time.Second); growth > 1 && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		growth = runtime.NumGoroutine() - before
	}
	return growth
}

func TestHungPanelViewRunsAtMostOnceAtATime(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	hung := testPanel("hung", 1, func(context.Context) (sep2admin.Descriptor, error) {
		calls.Add(1)
		<-release
		return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion}, nil
	})
	h := routerFor(t, 20*time.Millisecond, hung)

	if growth := goroutineGrowth(t, h, "/api/ui/panels/hung", http.StatusGatewayTimeout); growth > 1 {
		t.Fatalf("40 reads of a hung View left %d goroutines, want at most 1", growth)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("View invoked %d times, want 1", n)
	}
}

func TestOrdinaryPanelLeavesNoGoroutines(t *testing.T) {
	h := routerFor(t, time.Second, testPanel("fine", 1, okView))
	if growth := goroutineGrowth(t, h, "/api/ui/panels/fine", http.StatusOK); growth > 0 {
		t.Fatalf("40 reads of an ordinary View left %d goroutines, want 0", growth)
	}
}

func TestCanceledReadAnswers503(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	blocks := testPanel("blocks", 1, func(context.Context) (sep2admin.Descriptor, error) {
		close(started)
		<-release
		return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion}, nil
	})
	h := routerFor(t, 10*time.Second, blocks)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/ui/panels/blocks", nil).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Authorization", "Bearer "+panelTestKey)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(rec, req)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not return after the request was canceled")
	}
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "request canceled") {
		t.Fatalf("got %d %s, want 503 request canceled", rec.Code, rec.Body)
	}
}

// TestReadCanceledBeforeTheViewRunsFreesThePanel: a read refused before
// its View starts must not keep the panel's one slot.
func TestReadCanceledBeforeTheViewRunsFreesThePanel(t *testing.T) {
	h := routerFor(t, time.Second, testPanel("fine", 1, okView))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/ui/panels/fine", nil).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Authorization", "Bearer "+panelTestKey)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("canceled read = %d, want 503", rec.Code)
	}
	if rec := get(h, "/api/ui/panels/fine", true); rec.Code != http.StatusOK {
		t.Fatalf("next read = %d, want 200: the canceled read kept the panel's slot", rec.Code)
	}
}

func tableOf(rows int, cellText string) sep2admin.ViewFunc {
	return func(context.Context) (sep2admin.Descriptor, error) {
		rs := make([]sep2admin.Row, rows)
		for i := range rs {
			rs[i] = sep2admin.Row{sep2admin.TextCell(sep2admin.Value(cellText))}
		}
		return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion, Sections: []sep2admin.Section{{
			Body: sep2admin.NewTableBody(sep2admin.TableBody{Columns: []string{"c"}, Rows: rs}),
		}}}, nil
	}
}

func TestPanelResponseIsBounded(t *testing.T) {
	h := routerFor(t, 5*time.Second,
		testPanel("at-row-cap", 1, tableOf(maxPanelRows, "x")),
		testPanel("over-row-cap", 1, tableOf(maxPanelRows+1, "x")),
		testPanel("over-byte-cap", 1, tableOf(1, strings.Repeat("y", maxPanelBytes))),
	)
	if rec := get(h, "/api/ui/panels/at-row-cap", true); rec.Code != http.StatusOK {
		t.Fatalf("at the row cap = %d, want 200", rec.Code)
	}
	for _, id := range []string{"over-row-cap", "over-byte-cap"} {
		rec := get(h, "/api/ui/panels/"+id, true)
		if rec.Code != http.StatusInternalServerError || rec.Body.String() != `{"error":"panel response too large"}` {
			t.Errorf("%s = %d %.80s, want 500 with the fixed body", id, rec.Code, rec.Body)
		}
	}
}

// TestPanelIDsCannotShadowSPAPaths: a panel is served at /ui/<id>, and the
// SPA handler answers /ui/api... and its built files itself.
func TestPanelIDsCannotShadowSPAPaths(t *testing.T) {
	entries, err := fs.ReadDir(distFS, ".")
	if err != nil || len(entries) == 0 {
		t.Fatalf("read the SPA dist root: %d entries, %v", len(entries), err)
	}
	ids := []string{"api-status", "api"}
	for _, e := range entries {
		ids = append(ids, e.Name())
	}
	for _, id := range ids {
		if _, err := newPanelSet([]sep2admin.Panel{testPanel(id, 1, okView)}); !errors.Is(err, sep2admin.ErrInvalidID) {
			t.Errorf("panel ID %q: err = %v, want ErrInvalidID", id, err)
		}
	}
}
