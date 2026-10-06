package adminplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
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

func columnsDashboard(t *testing.T, collectTimeout time.Duration, srcs ...sep2admin.DeviceColumnSource) (columnsPayload, time.Duration) {
	t.Helper()
	mem := memory.NewEndDeviceStore()
	for id, lfdi := range map[string]string{"1": "AA", "2": "BB"} {
		if err := mem.Create(context.Background(), id, sep2.EndDevice{SFDI: "s" + id, LFDI: lfdi}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewDashboardHandler(dashboardTestStores(t, mem), "TLS").WithDeviceColumns(srcs...)
	if collectTimeout > 0 {
		h.collectTimeout = collectTimeout
	}
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

func TestDashboardColumns_PanickingColumnsKeepsRows(t *testing.T) {
	good := nameColumn(func(context.Context, []string) (map[string]map[string]string, error) {
		return map[string]map[string]string{"AA": {"name": "alpha"}}, nil
	})
	bad := panicColumns{}
	p, _ := columnsDashboard(t, 0, bad, good)
	if len(p.Devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(p.Devices))
	}
	if len(p.Columns) != 1 || p.Columns[0].ID != "name" {
		t.Errorf("columns = %+v, want only the healthy source's column", p.Columns)
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
	if elapsed > 2*time.Second {
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
