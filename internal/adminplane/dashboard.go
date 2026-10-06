package adminplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derstatus"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
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
	// Columns are the extra Devices-tab columns embedder sources supplied,
	// in registration order; each device's Cells holds one entry per column.
	// It is an empty list, never null, when no source is wired.
	Columns []DashboardColumn `json:"columns"`
	// Error is set when the device list could not be read. Devices is then
	// null rather than an empty list, so a failed read never looks like a
	// server with no devices.
	Error string `json:"error,omitempty"`
}

// DashboardColumn is one embedder-supplied Devices-tab column. Error is set
// when its source failed this pass, in which case every cell reads "-".
type DashboardColumn struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Error string `json:"error,omitempty"`
}

// noCell is what a cell shows when its source has nothing for the device.
const noCell = "-"

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
	// Cells maps a DashboardColumn ID to the text for this device. It is
	// never null: with no source wired it is empty.
	Cells map[string]string `json:"cells"`
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

	columnSources []*columnSourceState
	logMu         sync.Mutex
	logged        map[string]bool
	activity      *activity.Recorder
	offlineAfter  time.Duration
	now           func() time.Time
	// collectTimeout bounds one pass over the stores.
	collectTimeout time.Duration
	// columnTimeout bounds each embedder column source on its own.
	columnTimeout time.Duration
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
		columnTimeout:  defaultColumnTimeout,
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

// WithDeviceColumns registers embedder sources whose columns are added to
// every device row. Nil entries, typed nil ones included, are skipped. It
// returns d so construction chains.
func (d *DashboardHandler) WithDeviceColumns(srcs ...sep2admin.DeviceColumnSource) *DashboardHandler {
	d.columnSources = nil
	for _, s := range srcs {
		if !IsNilSource(s) {
			d.columnSources = append(d.columnSources, &columnSourceState{src: s})
		}
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
			Cells:       map[string]string{},
		})
	}

	sortDevicesByHref(devices)
	columns := d.fillColumns(parent, devices)

	uptime := time.Since(d.startTime).Round(time.Second)

	return DashboardData{
		Timestamp:   time.Now().Format("15:04:05"),
		DeviceCount: int(devCount),
		MUPCount:    int(mupCount),
		TLSMode:     d.tlsMode,
		Uptime:      uptime.String(),
		Devices:     devices,
		Columns:     columns,

		CommsOfflineAfterSeconds: int(d.offlineAfter / time.Second),
		Error:                    strings.Join(problems, "; "),
	}
}

// columnSourceState is one registered source plus what the dashboard keeps
// between passes: whether a call is still running, and the last columns and
// cells the source returned without error.
type columnSourceState struct {
	src sep2admin.DeviceColumnSource

	mu      sync.Mutex
	running bool
	// asked is what the running call's Columns returned, kept so a call that
	// times out in Cells still has named columns to show.
	asked []sep2admin.DeviceColumn
	cols  []sep2admin.DeviceColumn
	cells map[string]map[string]string
}

// columnResult is one pass's outcome for one source. cols may be empty when
// the source never answered Columns.
type columnResult struct {
	cols  []sep2admin.DeviceColumn
	cells map[string]map[string]string
	err   error
}

// defaultColumnTimeout bounds each embedder source on its own, separate from
// the store reads, so neither a slow store nor a slow source starves the
// other.
const defaultColumnTimeout = 2 * time.Second

// validColumnID limits column IDs to a set that is safe as a map key and as
// a test id in the UI.
var validColumnID = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// IsNilSource reports whether src is nil or an interface holding a nil
// pointer, map, slice or func, which would panic on first use.
func IsNilSource(src sep2admin.DeviceColumnSource) bool {
	if src == nil {
		return true
	}
	switch v := reflect.ValueOf(src); v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Interface, reflect.Chan:
		return v.IsNil()
	}
	return false
}

// call asks the source for its columns and cells. At most one call per
// source is in flight, across every pass and every client: a source that
// ignores ctx is not called again until its previous call returns, and busy
// is then true with the last good columns and cells. Otherwise the call runs
// in its own goroutine so a panic is recovered and the wait ends at ctx's
// deadline.
func (s *columnSourceState) call(ctx context.Context, lfdis []string) (res columnResult, busy bool) {
	s.mu.Lock()
	if s.running {
		res = columnResult{cols: s.cols, cells: s.cells}
		s.mu.Unlock()
		return res, true
	}
	s.running = true
	s.asked = nil
	s.mu.Unlock()

	done := make(chan columnResult, 1)
	go func() {
		var r columnResult
		defer func() {
			if p := recover(); p != nil {
				r.cells = nil
				r.err = fmt.Errorf("source panicked: %v", p)
			}
			s.mu.Lock()
			s.running = false
			if r.err == nil {
				s.cols, s.cells = r.cols, r.cells
			}
			s.mu.Unlock()
			done <- r
		}()
		r.cols = s.src.Columns()
		s.mu.Lock()
		s.asked = r.cols
		s.mu.Unlock()
		r.cells, r.err = s.src.Cells(ctx, lfdis)
		if r.err != nil {
			r.cells = nil
		}
	}()
	select {
	case r := <-done:
		return r, false
	case <-ctx.Done():
		s.mu.Lock()
		cols := s.asked
		s.mu.Unlock()
		return columnResult{cols: cols, err: fmt.Errorf("source did not answer in time: %w", ctx.Err())}, false
	}
}

// logOnce logs msg the first time it is seen, so a fixed configuration
// mistake does not repeat on every 5 s pass.
func (d *DashboardHandler) logOnce(msg string) {
	d.logMu.Lock()
	defer d.logMu.Unlock()
	if d.logged[msg] {
		return
	}
	if d.logged == nil {
		d.logged = map[string]bool{}
	}
	d.logged[msg] = true
	log.Print(msg)
}

// fillColumns runs every source concurrently, each under its own
// columnTimeout bound derived from parent, and writes the columns and cells
// into devices. Each pass calls every source once per client, so a source
// serves one call per SSE client per 5 s tick. A source's failure stays on
// its own columns: they read "-" (cells returned alongside an error are
// discarded) and carry the error, and no row is touched otherwise. A source
// whose columns are unknown, or all refused, shows one placeholder column
// "source-N" carrying the error. The returned list is never nil.
func (d *DashboardHandler) fillColumns(parent context.Context, devices []DashboardDevice) []DashboardColumn {
	columns := []DashboardColumn{}
	if len(d.columnSources) == 0 {
		return columns
	}
	lfdis := make([]string, len(devices))
	for i, dev := range devices {
		lfdis[i] = dev.LFDI
	}
	results := make([]columnResult, len(d.columnSources))
	busy := make([]bool, len(d.columnSources))
	var wg sync.WaitGroup
	for i, st := range d.columnSources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(parent, d.columnTimeout)
			defer cancel()
			results[i], busy[i] = st.call(ctx, lfdis)
		}()
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, res := range results {
		errText := ""
		switch {
		case busy[i]:
			errText = "source still busy with the previous call"
		case res.err != nil:
			errText = res.err.Error()
			d.logOnce("dashboard: device column source " + strconv.Itoa(i+1) + ": " + errText)
		}
		var problems []string
		var mine []int
		for _, c := range res.cols {
			switch {
			case c.ID == "":
				problems = append(problems, "column with an empty id ignored")
			case !validColumnID.MatchString(c.ID):
				problems = append(problems, fmt.Sprintf("column id %q ignored: use lowercase letters, digits, - and _", c.ID))
			case seen[c.ID]:
				problems = append(problems, fmt.Sprintf("duplicate column id %q ignored", c.ID))
			default:
				seen[c.ID] = true
				columns = append(columns, DashboardColumn{ID: c.ID, Label: c.Label})
				mine = append(mine, len(columns)-1)
			}
		}
		for _, p := range problems {
			d.logOnce("dashboard: device column source " + strconv.Itoa(i+1) + ": " + p)
		}
		if len(mine) == 0 && (errText != "" || len(problems) > 0 || len(res.cols) == 0) {
			id := "source-" + strconv.Itoa(i+1)
			columns = append(columns, DashboardColumn{ID: id, Label: "Source " + strconv.Itoa(i+1)})
			mine = append(mine, len(columns)-1)
			seen[id] = true
		}
		if len(mine) == 0 {
			continue
		}
		errText = strings.Join(append(nonEmpty(errText), problems...), "; ")
		for _, ci := range mine {
			columns[ci].Error = errText
			for j := range devices {
				text := noCell
				if v, ok := res.cells[devices[j].LFDI][columns[ci].ID]; ok {
					text = v
				}
				devices[j].Cells[columns[ci].ID] = text
			}
		}
	}
	return columns
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
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
