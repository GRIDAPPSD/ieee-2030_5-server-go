package adminplane

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// DashboardData is the payload sent to the dashboard via SSE.
type DashboardData struct {
	Timestamp   string            `json:"timestamp"`
	DeviceCount int               `json:"deviceCount"`
	MUPCount    int               `json:"mupCount"`
	TLSMode     string            `json:"tlsMode"`
	Uptime      string            `json:"uptime"`
	Devices     []DashboardDevice `json:"devices"`
	// CommsOfflineAfterSeconds is the threshold each device's Comms was
	// judged against.
	CommsOfflineAfterSeconds int `json:"commsOfflineAfterSeconds"`
	// Error is set when the device list could not be read. Devices is then
	// null rather than an empty list, so a failed read never looks like a
	// server with no devices.
	Error string `json:"error,omitempty"`
}

// DashboardDevice represents a device in the dashboard.
type DashboardDevice struct {
	SFDI string `json:"sfdi"`
	LFDI string `json:"lfdi"`
	Href string `json:"href"`
	// Enabled is null when the EndDevice has no enabled flag, so a UI can
	// tell "not set" apart from "disabled".
	Enabled *bool `json:"enabled"`
	// LastRequest is the server-clock time (RFC 3339, UTC) of the device's
	// last request, null when none was recorded since the server started.
	LastRequest *string `json:"lastRequest"`
	// Comms is one of the activity.Comms values: online, offline, not_seen,
	// or unknown when no recorder is wired.
	Comms string `json:"comms"`
}

// DashboardHandler serves the admin dashboard and SSE endpoint.
type DashboardHandler struct {
	stores    *Stores
	startTime time.Time
	tlsMode   string
	legacyUI  bool

	activity     *activity.Recorder
	offlineAfter time.Duration
	now          func() time.Time
}

// NewDashboardHandler creates a dashboard handler backed by the server's
// stores. legacyUI selects which page GET / answers with: false serves the
// embedded admin UI, true serves the pre-Svelte string-constant dashboard
// (see handleDashboardPage).
func NewDashboardHandler(stores *Stores, tlsMode string, legacyUI bool) *DashboardHandler {
	return &DashboardHandler{
		stores:    stores,
		startTime: time.Now(),
		tlsMode:   tlsMode,
		legacyUI:  legacyUI,

		offlineAfter: activity.DefaultOfflineAfter,
		now:          time.Now,
	}
}

// WithActivity makes the dashboard report each device's comms state from rec,
// judged against offlineAfter (zero or negative takes activity.DefaultOfflineAfter). A
// nil rec leaves every device "unknown". It returns d so construction chains.
func (d *DashboardHandler) WithActivity(rec *activity.Recorder, offlineAfter time.Duration) *DashboardHandler {
	d.activity = rec
	d.offlineAfter = offlineAfter
	if d.offlineAfter <= 0 {
		d.offlineAfter = activity.DefaultOfflineAfter
	}
	return d
}

// RegisterRoutes adds dashboard routes to the admin mux. Accepts the
// #272 routeRegistrar interface (satisfied by both *http.ServeMux
// and *recordingMux) so dashboard patterns participate in the boot-
// time route enumeration.
func (d *DashboardHandler) RegisterRoutes(mux routeRegistrar) {
	mux.HandleFunc("GET /", d.handleDashboardPage)
	mux.HandleFunc("GET /dashboard/events", d.handleSSE)
	mux.HandleFunc("GET /dashboard/data", d.handleData)
}

func (d *DashboardHandler) handleData(w http.ResponseWriter, r *http.Request) {
	data := d.collectData()
	w.Header().Set("Content-Type", "application/json")
	if data.Error != "" {
		w.WriteHeader(http.StatusInternalServerError)
	}
	_ = json.NewEncoder(w).Encode(data)
}

func (d *DashboardHandler) handleSSE(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Send initial data
	data := d.collectData()
	jsonData, _ := json.Marshal(data)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", jsonData)
	flusher.Flush()

	// Send updates every 5 seconds
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			data := d.collectData()
			jsonData, _ := json.Marshal(data)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", jsonData)
			flusher.Flush()
		}
	}
}

func (d *DashboardHandler) collectData() DashboardData {
	ctx := context.Background()
	var problems []string
	note := func(what string, err error) {
		log.Printf("dashboard: %s unavailable: %v", what, err)
		problems = append(problems, what+" unavailable: "+err.Error())
	}
	devCount, err := d.stores.EndDevices.Count(ctx)
	if err != nil {
		note("device count", err)
	}
	mupCount, err := d.stores.MirrorUsagePoints.Count(ctx)
	if err != nil {
		note("MUP count", err)
	}

	// Unbounded: the dashboard lists every device, so none is dropped
	// silently past a page size.
	result, listErr := d.stores.EndDevices.List(ctx, store.ListOptions{Unbounded: true})
	var devices []DashboardDevice
	now := d.now()
	if listErr != nil {
		note("device list", listErr)
	}
	for _, dev := range result.Items {
		var enabled *bool
		if dev.Enabled != nil {
			v := *dev.Enabled
			enabled = &v
		}
		var lastRequest *string
		if at, _, ok := d.activity.Last(dev.LFDI); ok {
			v := at.UTC().Format(time.RFC3339)
			lastRequest = &v
		}
		devices = append(devices, DashboardDevice{
			SFDI:        dev.SFDI,
			LFDI:        dev.LFDI,
			Href:        dev.Href,
			Enabled:     enabled,
			LastRequest: lastRequest,
			Comms:       string(d.activity.State(dev.LFDI, now, d.offlineAfter)),
		})
	}

	uptime := time.Since(d.startTime).Round(time.Second)

	return DashboardData{
		Timestamp:   time.Now().Format("15:04:05"),
		DeviceCount: int(devCount),
		MUPCount:    int(mupCount),
		TLSMode:     d.tlsMode,
		Uptime:      uptime.String(),
		Devices:     devices,

		CommsOfflineAfterSeconds: int(d.offlineAfter / time.Second),
		Error:                    strings.Join(problems, "; "),
	}
}

// handleDashboardPage answers GET / with one of two pages. By default it
// serves the embedded admin UI (pkg/adminui/web). With the legacy
// flag set (SEP2_ADMIN_LEGACY_DASHBOARD) it serves the string-constant
// dashboard in dashboard_html.go instead, so an operator can fall back to
// the previous page without downgrading the binary.
//
// The JSON and SSE routes below are shared: both pages read the same
// /dashboard/data and /dashboard/events.
func (d *DashboardHandler) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if d.legacyUI {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(dashboardHTML))
		return
	}
	serveSPAIndex(w, r)
}
