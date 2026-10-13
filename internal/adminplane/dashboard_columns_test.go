package adminplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

type columnsPayload struct {
	Columns []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Error string `json:"error"`
	} `json:"columns"`
	Devices []struct {
		LFDI  string            `json:"lfdi"`
		Cells map[string]string `json:"cells"`
	} `json:"devices"`
	Error string `json:"error"`
}

type fakeSource struct {
	cols  []sep2admin.DeviceColumn
	cells func(ctx context.Context, lfdis []string) (map[string]map[string]string, error)
}

func (f fakeSource) Columns() []sep2admin.DeviceColumn { return f.cols }
func (f fakeSource) Cells(ctx context.Context, lfdis []string) (map[string]map[string]string, error) {
	return f.cells(ctx, lfdis)
}

func nameColumn(cells func(context.Context, []string) (map[string]map[string]string, error)) fakeSource {
	return fakeSource{cols: []sep2admin.DeviceColumn{{ID: "name", Label: "Name"}}, cells: cells}
}

func columnsHandler(t *testing.T, columnTimeout time.Duration, srcs ...sep2admin.DeviceColumnSource) *DashboardHandler {
	t.Helper()
	mem := memory.NewEndDeviceStore()
	for id, lfdi := range map[string]string{"1": "AA", "2": "BB"} {
		if err := mem.Create(context.Background(), id, sep2.EndDevice{SFDI: "s" + id, LFDI: lfdi}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewDashboardHandler(dashboardTestStores(t, mem), "TLS").WithDeviceColumns(srcs...)
	if columnTimeout > 0 {
		h.columnTimeout = columnTimeout
	}
	return h
}

func columnsDashboard(t *testing.T, columnTimeout time.Duration, srcs ...sep2admin.DeviceColumnSource) (columnsPayload, time.Duration) {
	t.Helper()
	return columnsPass(t, columnsHandler(t, columnTimeout, srcs...))
}

func columnsPass(t *testing.T, h *DashboardHandler) (columnsPayload, time.Duration) {
	t.Helper()
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	start := time.Now()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard/data", nil))
	elapsed := time.Since(start)
	var p columnsPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return p, elapsed
}

func assertRowsKeptWithDash(t *testing.T, p columnsPayload) {
	t.Helper()
	if len(p.Devices) != 2 {
		t.Fatalf("devices = %d, want both rows kept", len(p.Devices))
	}
	for _, d := range p.Devices {
		if got := d.Cells["name"]; got != "-" {
			t.Errorf("device %s cell name = %q, want the \"-\" marker", d.LFDI, got)
		}
	}
	if len(p.Columns) != 1 || p.Columns[0].ID != "name" || p.Columns[0].Error == "" {
		t.Errorf("columns = %+v, want the name column carrying an error", p.Columns)
	}
}

func TestDashboardColumns_NoSource(t *testing.T) {
	mem := memory.NewEndDeviceStore()
	if err := mem.Create(context.Background(), "1", sep2.EndDevice{SFDI: "s", LFDI: "AA"}); err != nil {
		t.Fatal(err)
	}
	_, body := getDashboardData(t, dashboardTestStores(t, mem))
	cols, ok := body["columns"].([]any)
	if !ok || len(cols) != 0 {
		t.Fatalf("columns = %#v, want an empty non-null list", body["columns"])
	}
	dev := body["devices"].([]any)[0].(map[string]any)
	if cells, ok := dev["cells"].(map[string]any); !ok || len(cells) != 0 {
		t.Errorf("cells = %#v, want an empty non-null object", dev["cells"])
	}
	for _, k := range []string{"sfdi", "lfdi", "href", "enabled", "lastRequest", "comms", "lastKnown", "ders"} {
		if _, ok := dev[k]; !ok {
			t.Errorf("existing device field %q missing", k)
		}
	}
}

func TestDashboardColumns_OneSource(t *testing.T) {
	var gotLFDIs []string
	src := nameColumn(func(_ context.Context, lfdis []string) (map[string]map[string]string, error) {
		gotLFDIs = lfdis
		return map[string]map[string]string{"AA": {"name": "alpha"}}, nil
	})
	p, _ := columnsDashboard(t, 0, src)
	if !reflect.DeepEqual(gotLFDIs, []string{"AA", "BB"}) && !reflect.DeepEqual(gotLFDIs, []string{"BB", "AA"}) {
		t.Errorf("Cells lfdis = %v, want AA and BB", gotLFDIs)
	}
	if len(p.Columns) != 1 || p.Columns[0].ID != "name" || p.Columns[0].Label != "Name" || p.Columns[0].Error != "" {
		t.Fatalf("columns = %+v", p.Columns)
	}
	want := map[string]string{"AA": "alpha", "BB": "-"}
	for _, d := range p.Devices {
		if d.Cells["name"] != want[d.LFDI] {
			t.Errorf("device %s cell = %q, want %q", d.LFDI, d.Cells["name"], want[d.LFDI])
		}
	}
	if p.Error != "" {
		t.Errorf("payload error = %q, want none", p.Error)
	}
}

func TestDashboardColumns_FailingSourceKeepsRows(t *testing.T) {
	src := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		return nil, errors.New("registry down")
	})
	p, _ := columnsDashboard(t, 0, src)
	assertRowsKeptWithDash(t, p)
	if p.Columns[0].Error != "registry down" {
		t.Errorf("column error = %q", p.Columns[0].Error)
	}
}

func TestDashboardColumns_PanickingSourceKeepsRows(t *testing.T) {
	src := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		panic("boom")
	})
	p, _ := columnsDashboard(t, 0, src)
	assertRowsKeptWithDash(t, p)
}

func TestDashboardColumns_PanickingColumnsShowsError(t *testing.T) {
	good := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		return map[string]map[string]string{"AA": {"name": "alpha"}}, nil
	})
	p, _ := columnsDashboard(t, 0, panicColumns{}, good)
	if len(p.Devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(p.Devices))
	}
	if len(p.Columns) != 2 || p.Columns[0].ID != "~source-1" || p.Columns[1].ID != "name" {
		t.Fatalf("columns = %+v, want a source-1 placeholder then name", p.Columns)
	}
	if !strings.Contains(p.Columns[0].Error, "panicked") || p.Columns[1].Error != "" {
		t.Errorf("column errors = %q / %q, want the panic shown on the placeholder only", p.Columns[0].Error, p.Columns[1].Error)
	}
	for _, d := range p.Devices {
		if d.Cells["~source-1"] != "-" {
			t.Errorf("%s placeholder cell = %q, want -", d.LFDI, d.Cells["~source-1"])
		}
	}
}

type blockingColumns struct{ release chan struct{} }

func (b blockingColumns) Columns() []sep2admin.DeviceColumn {
	<-b.release
	return nil
}
func (blockingColumns) Cells(context.Context, []string) (map[string]map[string]string, error) {
	return nil, nil
}

func TestDashboardColumns_BlockingColumnsBounded(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	p, elapsed := columnsDashboard(t, 100*time.Millisecond, blockingColumns{release})
	if elapsed > time.Second {
		t.Fatalf("pass took %v, want it bounded near 100ms", elapsed)
	}
	if len(p.Devices) != 2 || len(p.Columns) != 1 || p.Columns[0].Error == "" {
		t.Errorf("devices %d, columns %+v, want rows kept and an errored placeholder", len(p.Devices), p.Columns)
	}
}

type panicColumns struct{}

func (panicColumns) Columns() []sep2admin.DeviceColumn { panic("columns boom") }
func (panicColumns) Cells(context.Context, []string) (map[string]map[string]string, error) {
	return nil, nil
}

func TestDashboardColumns_SlowSourceBoundedByContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	src := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		<-release
		return nil, nil
	})
	p, elapsed := columnsDashboard(t, 100*time.Millisecond, src)
	if elapsed > time.Second {
		t.Fatalf("pass took %v, want it bounded near the 100ms context", elapsed)
	}
	assertRowsKeptWithDash(t, p)
}

func TestDashboardColumns_TwoSourcesOneFails(t *testing.T) {
	ok := fakeSource{
		cols: []sep2admin.DeviceColumn{{ID: "identity", Label: "Identity"}},
		cells: func(context.Context, []string) (map[string]map[string]string, error) {
			return map[string]map[string]string{"AA": {"identity": "cert"}}, nil
		},
	}
	bad := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		return nil, errors.New("nope")
	})
	p, _ := columnsDashboard(t, 0, bad, ok)
	if len(p.Columns) != 2 || p.Columns[0].ID != "name" || p.Columns[1].ID != "identity" {
		t.Fatalf("columns = %+v, want name then identity in registration order", p.Columns)
	}
	for _, d := range p.Devices {
		if d.Cells["name"] != "-" {
			t.Errorf("%s name = %q", d.LFDI, d.Cells["name"])
		}
		if d.LFDI == "AA" && d.Cells["identity"] != "cert" {
			t.Errorf("healthy source cell = %q, want cert", d.Cells["identity"])
		}
	}
}

func TestDashboardColumns_IgnoringSourceHasOneCallInFlight(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	src := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		calls.Add(1)
		<-release
		return nil, nil
	})
	h := columnsHandler(t, 20*time.Millisecond, src)
	before := runtime.NumGoroutine()
	var last columnsPayload
	for range 12 {
		last, _ = columnsPass(t, h)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("Cells called %d times over 12 passes, want 1 while the first is in flight", n)
	}
	if grew := runtime.NumGoroutine() - before; grew > 3 {
		t.Errorf("goroutines grew by %d over 12 passes, want a bounded few", grew)
	}
	if len(last.Columns) != 1 || !strings.Contains(last.Columns[0].Error, "busy") {
		t.Errorf("columns = %+v, want a still-busy error", last.Columns)
	}
	if len(last.Devices) != 2 {
		t.Errorf("devices = %d, want rows kept", len(last.Devices))
	}
}

func TestDashboardColumns_BusyKeepsLastGoodCells(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var calls atomic.Int32
	src := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		if calls.Add(1) == 1 {
			return map[string]map[string]string{"AA": {"name": "alpha"}}, nil
		}
		<-release
		return nil, nil
	})
	h := columnsHandler(t, 30*time.Millisecond, src)
	columnsPass(t, h)
	columnsPass(t, h) // times out, call stays in flight
	p, _ := columnsPass(t, h)
	if len(p.Columns) != 1 || !strings.Contains(p.Columns[0].Error, "busy") {
		t.Fatalf("columns = %+v, want still busy", p.Columns)
	}
	for _, d := range p.Devices {
		want := map[string]string{"AA": "alpha", "BB": "-"}[d.LFDI]
		if d.Cells["name"] != want {
			t.Errorf("%s cell = %q, want last good %q", d.LFDI, d.Cells["name"], want)
		}
	}
}

func TestDashboardColumns_SlowSourceDoesNotStarveFast(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	slow := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		<-release
		return nil, nil
	})
	fast := fakeSource{
		cols: []sep2admin.DeviceColumn{{ID: "identity", Label: "Identity"}},
		cells: func(context.Context, []string) (map[string]map[string]string, error) {
			return map[string]map[string]string{"AA": {"identity": "cert"}}, nil
		},
	}
	p, _ := columnsDashboard(t, 100*time.Millisecond, slow, fast)
	if len(p.Columns) != 2 || p.Columns[1].Error != "" {
		t.Fatalf("columns = %+v, want the fast source clean", p.Columns)
	}
	for _, d := range p.Devices {
		if d.LFDI == "AA" && d.Cells["identity"] != "cert" {
			t.Errorf("fast cell = %q, want cert", d.Cells["identity"])
		}
	}
}

func TestDashboardColumns_ErrorDiscardsReturnedCells(t *testing.T) {
	src := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		return map[string]map[string]string{"AA": {"name": "alpha"}}, errors.New("partial")
	})
	p, _ := columnsDashboard(t, 0, src)
	assertRowsKeptWithDash(t, p)
}

func TestDashboardColumns_InvalidAndDuplicateIDsShowOnColumnAndLogOnce(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	src := fakeSource{
		cols: []sep2admin.DeviceColumn{
			{ID: "constructor", Label: "Ctor"},
			{ID: "constructor", Label: "Again"},
			{ID: "", Label: "Empty"},
			{ID: "Bad Id", Label: "Bad"},
		},
		cells: func(context.Context, []string) (map[string]map[string]string, error) {
			return map[string]map[string]string{"AA": {"constructor": "x"}}, nil
		},
	}
	h := columnsHandler(t, 0, src)
	p, _ := columnsPass(t, h)
	columnsPass(t, h)
	if len(p.Columns) != 1 || p.Columns[0].ID != "constructor" {
		t.Fatalf("columns = %+v, want only the first valid one", p.Columns)
	}
	for _, want := range []string{"duplicate", "empty", "Bad Id"} {
		if !strings.Contains(p.Columns[0].Error, want) {
			t.Errorf("column error %q lacks %q", p.Columns[0].Error, want)
		}
	}
	if n := strings.Count(buf.String(), "duplicate"); n != 1 {
		t.Errorf("duplicate logged %d times over 2 passes, want once", n)
	}
}

func TestDashboardColumns_PlaceholderIDNeverCollidesWithRealID(t *testing.T) {
	real := fakeSource{
		cols: []sep2admin.DeviceColumn{{ID: "source-2", Label: "Real"}},
		cells: func(context.Context, []string) (map[string]map[string]string, error) {
			return map[string]map[string]string{"AA": {"source-2": "real-value"}}, nil
		},
	}
	failing := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		return nil, errors.New("down")
	})
	failing.cols = nil // columns unknown, so source 2 shows a placeholder
	p, _ := columnsDashboard(t, 0, real, failing)
	seen := map[string]bool{}
	for _, c := range p.Columns {
		if seen[c.ID] {
			t.Fatalf("duplicate column id %q in %+v", c.ID, p.Columns)
		}
		seen[c.ID] = true
	}
	if len(p.Columns) != 2 || p.Columns[0].ID != "source-2" {
		t.Fatalf("columns = %+v, want the real column then a placeholder", p.Columns)
	}
	for _, d := range p.Devices {
		if d.LFDI == "AA" && d.Cells["source-2"] != "real-value" {
			t.Errorf("real column cell = %q, want real-value", d.Cells["source-2"])
		}
	}
}

func TestUniqueColumnIDsDropsLaterDuplicates(t *testing.T) {
	got := uniqueColumnIDs([]DashboardColumn{{ID: "a", Label: "1"}, {ID: "b"}, {ID: "a", Label: "2"}})
	if len(got) != 2 || got[0].Label != "1" || got[1].ID != "b" {
		t.Errorf("uniqueColumnIDs = %+v", got)
	}
}

func TestDashboardColumns_SlowHealthySourceSharedByConcurrentClients(t *testing.T) {
	var calls atomic.Int32
	src := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		calls.Add(1)
		time.Sleep(200 * time.Millisecond)
		return map[string]map[string]string{"AA": {"name": "alpha"}}, nil
	})
	h := columnsHandler(t, 0, src)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	var wg sync.WaitGroup
	bodies := make([]string, 2)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard/data", nil))
			bodies[i] = w.Body.String()
		}()
	}
	wg.Wait()
	for i, b := range bodies {
		var p columnsPayload
		if err := json.Unmarshal([]byte(b), &p); err != nil {
			t.Fatalf("client %d body %q: %v", i, b, err)
		}
		if len(p.Columns) != 1 || p.Columns[0].ID != "name" || p.Columns[0].Error != "" {
			t.Errorf("client %d columns = %+v, want name with no error", i, p.Columns)
		}
		got := ""
		for _, d := range p.Devices {
			if d.LFDI == "AA" {
				got = d.Cells["name"]
			}
		}
		if got != "alpha" {
			t.Errorf("client %d cell = %q, want alpha", i, got)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("Cells called %d times for 2 concurrent clients, want 1 shared call", n)
	}
}

func TestDashboardColumns_BusyErrorNamesAge(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	src := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		<-release
		return nil, nil
	})
	h := columnsHandler(t, 30*time.Millisecond, src)
	columnsPass(t, h)
	p, _ := columnsPass(t, h)
	if len(p.Columns) != 1 || !strings.Contains(p.Columns[0].Error, "busy") || !strings.Contains(p.Columns[0].Error, "running for") {
		t.Errorf("columns = %+v, want a busy error naming how long the call has run", p.Columns)
	}
}

func TestDashboardColumns_LogOnceBoundedAndRearmed(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	var n atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	src := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		if fail.Load() {
			return nil, fmt.Errorf("down %d", n.Add(1))
		}
		return map[string]map[string]string{"AA": {"name": "alpha"}}, nil
	})
	h := columnsHandler(t, 0, src)
	for range 50 {
		columnsPass(t, h)
	}
	if got := strings.Count(buf.String(), "device column source 1"); got != 1 {
		t.Errorf("logged %d lines for 50 distinct error texts, want 1", got)
	}
	h.logMu.Lock()
	size := len(h.logged)
	h.logMu.Unlock()
	if size != 1 {
		t.Errorf("log-once table holds %d entries after 50 passes, want 1", size)
	}
	fail.Store(false)
	columnsPass(t, h)
	h.logMu.Lock()
	size = len(h.logged)
	h.logMu.Unlock()
	if size != 0 {
		t.Errorf("table holds %d entries after the source recovered, want 0", size)
	}
	fail.Store(true)
	columnsPass(t, h)
	if got := strings.Count(buf.String(), "device column source 1"); got != 2 {
		t.Errorf("logged %d lines after a recover-and-fail-again, want 2", got)
	}
}
