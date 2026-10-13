package adminplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

type commsDevice struct {
	LFDI        string  `json:"lfdi"`
	Comms       string  `json:"comms"`
	LastRequest *string `json:"lastRequest"`
}

type commsPayload struct {
	Devices                  []commsDevice `json:"devices"`
	CommsOfflineAfterSeconds int           `json:"commsOfflineAfterSeconds"`
}

func commsDashboard(t *testing.T, rec *activity.Recorder, offlineAfter time.Duration, now time.Time) commsPayload {
	t.Helper()
	mem := memory.NewEndDeviceStore()
	for id, lfdi := range map[string]string{"1": "seen", "2": "never"} {
		if err := mem.Create(context.Background(), id, sep2.EndDevice{SFDI: "s" + id, LFDI: lfdi}); err != nil {
			t.Fatal(err)
		}
	}
	h := NewDashboardHandler(dashboardTestStores(t, mem), "TLS").WithActivity(rec, offlineAfter)
	h.now = func() time.Time { return now }
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/dashboard/data", nil))
	var p commsPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return p
}

func byLFDI(t *testing.T, p commsPayload) map[string]commsDevice {
	t.Helper()
	m := map[string]commsDevice{}
	for _, d := range p.Devices {
		m[d.LFDI] = d
	}
	return m
}

func TestDashboardComms_ThresholdBoundary(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rec := activity.NewWithClock(func() time.Time { return t0 })
	rec.Record("seen")

	cases := []struct {
		name string
		now  time.Time
		want string
	}{
		{"threshold minus 1s is online", t0.Add(activity.DefaultOfflineAfter - time.Second), "online"},
		{"at threshold is offline", t0.Add(activity.DefaultOfflineAfter), "offline"},
	}
	for _, tc := range cases {
		p := commsDashboard(t, rec, 0, tc.now)
		got := byLFDI(t, p)["seen"]
		if got.Comms != tc.want {
			t.Errorf("%s: comms = %q, want %q", tc.name, got.Comms, tc.want)
		}
		if got.LastRequest == nil || *got.LastRequest != "2026-10-05T12:00:00Z" {
			t.Errorf("%s: lastRequest = %v, want 2026-10-05T12:00:00Z", tc.name, got.LastRequest)
		}
		if p.CommsOfflineAfterSeconds != 300 {
			t.Errorf("%s: commsOfflineAfterSeconds = %d, want the 300 default", tc.name, p.CommsOfflineAfterSeconds)
		}
		never := byLFDI(t, p)["never"]
		if never.Comms != "not_seen" || never.LastRequest != nil {
			t.Errorf("%s: unseen device = %+v, want not_seen with null lastRequest", tc.name, never)
		}
	}
}

func TestDashboardComms_ConfiguredThreshold(t *testing.T) {
	t0 := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rec := activity.NewWithClock(func() time.Time { return t0 })
	rec.Record("seen")
	p := commsDashboard(t, rec, 60*time.Second, t0.Add(60*time.Second))
	if got := byLFDI(t, p)["seen"].Comms; got != "offline" {
		t.Errorf("comms at a 60s threshold after 60s = %q, want offline", got)
	}
	if p.CommsOfflineAfterSeconds != 60 {
		t.Errorf("commsOfflineAfterSeconds = %d, want 60", p.CommsOfflineAfterSeconds)
	}
}

func TestDashboardComms_NilRecorderIsUnknown(t *testing.T) {
	p := commsDashboard(t, nil, 0, time.Now())
	for lfdi, d := range byLFDI(t, p) {
		if d.Comms != "unknown" || d.LastRequest != nil {
			t.Errorf("%s with no recorder = %+v, want unknown with null lastRequest", lfdi, d)
		}
	}
	if len(p.Devices) != 2 {
		t.Errorf("devices = %d, want 2", len(p.Devices))
	}
}

// The recorder's clock may be in any zone; lastRequest is always UTC, so a
// UI parses one form.
func TestDashboardComms_LastRequestIsUTCFromANonUTCClock(t *testing.T) {
	zone := time.FixedZone("UTC+5", 5*3600)
	at := time.Date(2026, 10, 5, 17, 0, 0, 0, zone)
	rec := activity.NewWithClock(func() time.Time { return at })
	rec.Record("seen")
	p := commsDashboard(t, rec, 0, at)
	got := byLFDI(t, p)["seen"].LastRequest
	if got == nil || *got != "2026-10-05T12:00:00Z" {
		t.Errorf("lastRequest = %v, want 2026-10-05T12:00:00Z", got)
	}
}

// A stored EndDevice may hold its LFDI in lowercase while the certificate
// yields uppercase; the device must still read online.
func TestDashboardComms_StoredLowercaseLFDIMatchesCertificateCase(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	rec := activity.NewWithClock(func() time.Time { return now })
	rec.Record("ABCDEF0123456789ABCDEF0123456789ABCDEF01")

	mem := memory.NewEndDeviceStore()
	if err := mem.Create(context.Background(), "1", sep2.EndDevice{SFDI: "s1", LFDI: "abcdef0123456789abcdef0123456789abcdef01"}); err != nil {
		t.Fatal(err)
	}
	h := NewDashboardHandler(dashboardTestStores(t, mem), "TLS").WithActivity(rec, 0)
	h.now = func() time.Time { return now }
	d := h.collectData(context.Background()).Devices
	if len(d) != 1 || d[0].Comms != "online" {
		t.Errorf("devices = %+v, want one online device", d)
	}
}
