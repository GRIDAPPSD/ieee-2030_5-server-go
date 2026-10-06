package adminplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derstatus"
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
	// LastKnown is true when Comms says the device is not currently talking
	// (offline or not seen since start), so its DER status is what it last
	// reported rather than a live reading. It stays false for "unknown".
	LastKnown bool `json:"lastKnown"`
	// DERs is never null: a device with no DERs gets an empty list.
	DERs []DashboardDER `json:"ders"`
	// DERError is set when this device's DER data could not be read in full:
	// DERs then holds only what did read, and an empty list with DERError set
	// means "unreadable", not "no DERs".
	DERError string `json:"derError,omitempty"`
}

// DashboardDER is one DER's reported status, decoded by internal/derstatus.
// Reported is false when the server holds no DERStatus for it, in which case
// the decoded fields are zero and mean nothing.
type DashboardDER struct {
	ID       string `json:"id"`
	Reported bool   `json:"reported"`
	derstatus.Decoded
}

// DashboardHandler serves the admin dashboard and SSE endpoint.
type DashboardHandler struct {
	stores    *Stores
	startTime time.Time
	tlsMode   string

	activity     *activity.Recorder
	offlineAfter time.Duration
	now          func() time.Time
	// collectTimeout bounds one pass over the stores.
	collectTimeout time.Duration
}

// defaultCollectTimeout keeps a stalled store from holding a dashboard pass
// past the 5 s tick.
const defaultCollectTimeout = 4 * time.Second

// NewDashboardHandler creates a dashboard handler backed by the server's
// stores.
func NewDashboardHandler(stores *Stores, tlsMode string) *DashboardHandler {
	return &DashboardHandler{
		stores:    stores,
		startTime: time.Now(),
		tlsMode:   tlsMode,

		offlineAfter:   activity.DefaultOfflineAfter,
		now:            time.Now,
		collectTimeout: defaultCollectTimeout,
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
	data := d.collectData(r.Context())
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
	data := d.collectData(r.Context())
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
			data := d.collectData(ctx)
			jsonData, _ := json.Marshal(data)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", jsonData)
			flusher.Flush()
		}
	}
}

func (d *DashboardHandler) collectData(parent context.Context) DashboardData {
	ctx, cancel := context.WithTimeout(parent, d.collectTimeout)
	defer cancel()
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
		comms := d.activity.State(dev.LFDI, now, d.offlineAfter)
		ders, err := d.collectDERs(ctx, dev.Href, now.Unix())
		var derError string
		if err != nil {
			log.Printf("dashboard: DER status for %s: %v", dev.LFDI, err)
			derError = err.Error()
		}
		devices = append(devices, DashboardDevice{
			SFDI:        dev.SFDI,
			LFDI:        dev.LFDI,
			Href:        dev.Href,
			Enabled:     enabled,
			LastRequest: lastRequest,
			Comms:       string(comms),
			LastKnown:   comms == activity.Offline || comms == activity.NotSeen,
			DERs:        ders,
			DERError:    derError,
		})
	}

	sortDevicesByHref(devices)

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

// collectDERs reads every DER of the EndDevice at edevHref and decodes its
// DERStatus against now (Unix seconds). A DER with no status is listed with
// Reported false. A read failure never shortens the list silently: the DERs
// that did read are returned with a non-nil error naming each one that did
// not, and a List failure returns an empty list with the error. Absent DER
// stores yield an empty list and no error.
func (d *DashboardHandler) collectDERs(ctx context.Context, edevHref string, now int64) ([]DashboardDER, error) {
	out := []DashboardDER{}
	if store.IsAbsent(d.stores.DERs) || store.IsAbsent(d.stores.DERStatuses) {
		return out, nil
	}
	edevID := edevHref[strings.LastIndex(edevHref, "/")+1:]
	ders, err := d.stores.DERs.List(ctx, edevID, store.ListOptions{Unbounded: true})
	if err != nil {
		return out, fmt.Errorf("DERs.List(%q): %w", edevID, err)
	}
	var failed []string
	for _, der := range ders.Items {
		derID := der.Href[strings.LastIndex(der.Href, "/")+1:]
		entry := DashboardDER{ID: derID}
		s, err := d.stores.DERStatuses.Get(ctx, edevID+"/"+derID, derStatusKey)
		switch {
		case err == nil:
			entry.Reported = true
			entry.Decoded = derstatus.Decode(s, now, derstatus.DefaultPollRateSeconds)
		case errors.Is(err, store.ErrNotFound):
		default:
			failed = append(failed, fmt.Sprintf("DER %s status: %v", derID, err))
			continue
		}
		out = append(out, entry)
	}
	if len(failed) > 0 {
		return out, errors.New(strings.Join(failed, "; "))
	}
	return out, nil
}

// derStatusKey is the singleton key DERStatus is stored under.
const derStatusKey = "default"

// sortDevicesByHref orders devices by the trailing integer of their href, so
// /edev/2 precedes /edev/10. Hrefs with no trailing integer follow the
// numbered ones, ordered by href text. Doing it in the payload keeps every
// consumer of the dashboard feed in the same order.
func sortDevicesByHref(devices []DashboardDevice) {
	sort.SliceStable(devices, func(i, j int) bool {
		ni, oki := hrefNumber(devices[i].Href)
		nj, okj := hrefNumber(devices[j].Href)
		switch {
		case oki && okj && ni != nj:
			return ni < nj
		case oki != okj:
			return oki
		case !oki:
			return devices[i].Href < devices[j].Href
		}
		return false
	})
}

// hrefNumber returns the integer after the last "/" of href.
func hrefNumber(href string) (uint64, bool) {
	n, err := strconv.ParseUint(href[strings.LastIndex(href, "/")+1:], 10, 64)
	return n, err == nil
}

// handleDashboardPage answers GET / with the embedded admin UI
// (pkg/adminui/web), which reads /dashboard/data and /dashboard/events.
func (d *DashboardHandler) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	serveSPAIndex(w, r)
}
