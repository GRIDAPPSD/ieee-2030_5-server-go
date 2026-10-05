package adminplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// failingListEndDevices fails List only, so Count still answers and the
// dashboard's other fields stay populated around the failed device read.
type failingListEndDevices struct {
	store.EndDeviceStore
}

func (failingListEndDevices) List(context.Context, store.ListOptions) (store.ListResult[sep2.EndDevice], error) {
	return store.ListResult[sep2.EndDevice]{}, errors.New("backend down")
}

func dashboardTestStores(t *testing.T, devs store.EndDeviceStore) *Stores {
	t.Helper()
	return &Stores{
		EndDevices:        devs,
		MirrorUsagePoints: memory.NewStore[sep2.MirrorUsagePoint](),
	}
}

func getDashboardData(t *testing.T, s *Stores) (int, map[string]any) {
	t.Helper()
	h := NewDashboardHandler(s, "TLS", false)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/dashboard/data", nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestDashboardData_StoreErrorReachesPayloadAsError(t *testing.T) {
	mem := memory.NewEndDeviceStore()
	if err := mem.Create(context.Background(), "1", sep2.EndDevice{SFDI: "111", LFDI: "AA"}); err != nil {
		t.Fatal(err)
	}
	code, body := getDashboardData(t, dashboardTestStores(t, failingListEndDevices{mem}))

	if code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", code)
	}
	if got, _ := body["error"].(string); got != "device list unavailable: backend down" {
		t.Errorf("error = %q, want the store error named", got)
	}
	// null, not []: a failed read must not look like a server with no devices.
	if v, present := body["devices"]; !present || v != nil {
		t.Errorf("devices = %v (present=%v), want present and null", v, present)
	}
}

func TestDashboardData_ListsEveryDevicePastFiftyWithNoError(t *testing.T) {
	mem := memory.NewEndDeviceStore()
	const n = 120
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%03d", i)
		if err := mem.Create(context.Background(), id, sep2.EndDevice{SFDI: "sfdi" + id, LFDI: "lfdi" + id}); err != nil {
			t.Fatal(err)
		}
	}
	code, body := getDashboardData(t, dashboardTestStores(t, mem))

	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
	if _, present := body["error"]; present {
		t.Errorf("error present on a healthy read: %v", body["error"])
	}
	devs, _ := body["devices"].([]any)
	if len(devs) != n {
		t.Fatalf("devices = %d, want %d", len(devs), n)
	}
	seen := map[string]bool{}
	for _, d := range devs {
		seen[d.(map[string]any)["sfdi"].(string)] = true
	}
	if !seen["sfdi000"] || !seen["sfdi119"] {
		t.Errorf("first and last device must both be listed, got %d distinct sfdi", len(seen))
	}
	if got := body["deviceCount"].(float64); got != n {
		t.Errorf("deviceCount = %v, want %d", got, n)
	}
}

func TestDashboardSSE_FrameCarriesStoreError(t *testing.T) {
	h := NewDashboardHandler(dashboardTestStores(t, failingListEndDevices{memory.NewEndDeviceStore()}), "TLS", false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // handleSSE writes its first frame, then returns on the done context
	rec := httptest.NewRecorder()
	h.handleSSE(rec, httptest.NewRequest(http.MethodGet, "/dashboard/events", nil).WithContext(ctx))

	frame := strings.TrimSuffix(strings.TrimPrefix(rec.Body.String(), "data: "), "\n\n")
	var body map[string]any
	if err := json.Unmarshal([]byte(frame), &body); err != nil {
		t.Fatalf("frame %q is not JSON: %v", rec.Body.String(), err)
	}
	if got, _ := body["error"].(string); got != "device list unavailable: backend down" {
		t.Errorf("error = %q, want the store error named", got)
	}
	if v, present := body["devices"]; !present || v != nil {
		t.Errorf("devices = %v (present=%v), want present and null", v, present)
	}
}

// failingCountEndDevices fails Count only; List still serves.
type failingCountEndDevices struct {
	store.EndDeviceStore
}

func (failingCountEndDevices) Count(context.Context) (uint32, error) {
	return 0, errors.New("count down")
}

func TestDashboardData_CountErrorIsSurfacedNotDropped(t *testing.T) {
	mem := memory.NewEndDeviceStore()
	if err := mem.Create(context.Background(), "1", sep2.EndDevice{SFDI: "111", LFDI: "AA"}); err != nil {
		t.Fatal(err)
	}
	code, body := getDashboardData(t, dashboardTestStores(t, failingCountEndDevices{mem}))

	if code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", code)
	}
	if got, _ := body["error"].(string); got != "device count unavailable: count down" {
		t.Errorf("error = %q, want the count error named", got)
	}
}

func TestDashboardData_ListErrorIsLoggedOncePerRead(t *testing.T) {
	var buf strings.Builder
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	getDashboardData(t, dashboardTestStores(t, failingListEndDevices{memory.NewEndDeviceStore()}))

	if got := strings.Count(buf.String(), "dashboard: device list unavailable: backend down"); got != 1 {
		t.Errorf("list error logged %d times in %q, want 1", got, buf.String())
	}
}

func TestDashboardData_EnabledFlagKeepsNotSetApartFromFalse(t *testing.T) {
	yes, no := true, false
	mem := memory.NewEndDeviceStore()
	for id, enabled := range map[string]*bool{"a": &yes, "b": &no, "c": nil} {
		if err := mem.Create(context.Background(), id, sep2.EndDevice{SFDI: "sfdi-" + id, LFDI: "lfdi-" + id, Enabled: enabled}); err != nil {
			t.Fatal(err)
		}
	}
	_, body := getDashboardData(t, dashboardTestStores(t, mem))

	got := map[string]any{}
	for _, d := range body["devices"].([]any) {
		dev := d.(map[string]any)
		enabled, present := dev["enabled"]
		if !present {
			t.Fatalf("device %v has no enabled key", dev["sfdi"])
		}
		got[dev["sfdi"].(string)] = enabled
	}
	want := map[string]any{"sfdi-a": true, "sfdi-b": false, "sfdi-c": nil}
	for sfdi, w := range want {
		if got[sfdi] != w {
			t.Errorf("enabled for %s = %v, want %v", sfdi, got[sfdi], w)
		}
	}
}
