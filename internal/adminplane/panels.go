package adminplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

// panelViewTimeout bounds one View call. A View still running at the
// deadline answers 504; its goroutine is not killed (see InvokeView).
const panelViewTimeout = 5 * time.Second

// Response bounds. The shell renders every row into the page with no
// paging and re-polls an open panel, so a panel's payload stays small.
// The largest panel in view is a per-device registry, sized by the fleet
// (tens of devices); 1000 rows leaves an order of magnitude of headroom.
// 1 MiB holds 1000 rows at about 1 KiB each. Chart points are not rows:
// the encoder caps them per Descriptor, and only the byte cap applies here.
const (
	maxPanelRows  = 1000
	maxPanelBytes = 1 << 20
)

// panelSet is the frozen extension band an embedder registered: the
// panels the shell adds after its own tabs. The core band holds the
// shell's tabs, which the shell renders itself, so it is never served.
type panelSet struct {
	// actions is true when the plane serves panel actions
	// (panel_actions.go); actionLimit is their plane-wide rate, shared with
	// the schema read, and running counts the Run calls still executing.
	actions     bool
	actionLimit *tokenBucket
	running     *ActionTracker

	panels  []sep2admin.Panel
	busy    map[string]*atomic.Bool
	timeout time.Duration
	// openStreams counts this plane's open panel streams (panel_stream.go).
	openStreams atomic.Int32
	// streamsDone is Config.StreamsDone; nil never closes.
	streamsDone <-chan struct{}
}

// newPanelSet registers and freezes panels. Any refusal fails the build,
// so a misconfigured panel stops the boot instead of vanishing from the
// nav.
func newPanelSet(panels []sep2admin.Panel) (*panelSet, error) {
	r := sep2admin.NewRegistry()
	for _, p := range panels {
		if err := r.Register(p); err != nil {
			return nil, fmt.Errorf("register admin panel %q: %w", p.ID, err)
		}
	}
	frozen, err := r.Freeze()
	if err != nil {
		return nil, fmt.Errorf("freeze admin panels: %w", err)
	}
	ps := noPanels()
	for _, p := range frozen {
		if p.Placement.Extension() {
			ps.panels = append(ps.panels, p)
			ps.busy[p.ID] = new(atomic.Bool)
		}
	}
	return ps, nil
}

// noPanels is the set BuildAdminRouter mounts: the routes exist and list
// nothing.
func noPanels() *panelSet {
	return &panelSet{
		panels:      []sep2admin.Panel{},
		actionLimit: newTokenBucket(time.Now),
		running:     &ActionTracker{},
		busy:        map[string]*atomic.Bool{},
		timeout:     panelViewTimeout,
	}
}

type panelEntry struct {
	ID     string       `json:"id"`
	Label  string       `json:"label"`
	Picker *pickerEntry `json:"picker,omitempty"`
	Stream *streamEntry `json:"stream,omitempty"`
	// Actions is set only when the plane serves panel actions.
	Actions []actionEntry `json:"actions,omitempty"`
}

// streamEntry tells the shell the stream parameter's bounds, so it can
// refuse a value before asking.
type streamEntry struct {
	MaxLen  int    `json:"maxLen"`
	Charset string `json:"charset"`
}

type pickerEntry struct {
	Max int `json:"max"`
}

// handleList answers GET /api/ui/panels with [{id, label}] in frozen
// order, and [] when nothing is registered. A panel with a Picker also
// carries {"picker":{"max":N}}, one with a Stream
// {"stream":{"maxLen":N,"charset":"..."}}, and with actions on, one that has
// Actions {"actions":[{id,label}]}.
func (ps *panelSet) handleList() http.HandlerFunc {
	entries := make([]panelEntry, 0, len(ps.panels))
	for _, p := range ps.panels {
		e := panelEntry{ID: p.ID, Label: p.Label}
		if p.Picker != nil {
			e.Picker = &pickerEntry{Max: sep2admin.MaxSelection}
		}
		if p.Stream != nil {
			e.Stream = &streamEntry{MaxLen: p.Stream.Param.MaxLen, Charset: p.Stream.Param.Charset}
		}
		if ps.actions {
			e.Actions = actionEntries(p)
		}
		entries = append(entries, e)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		writePanelJSON(w, http.StatusOK, entries)
	}
}

// Selection query bounds. The raw query is capped before it is parsed.
const (
	maxSelectionQueryBytes = 2048
	selectionRefusal       = "invalid selection"
)

// parseSelection returns the sel values of a panel request. Any refusal
// is a bare error: the caller answers a fixed body, so no requested value
// can reach a response or a log line.
func parseSelection(p sep2admin.Panel, rawQuery string) ([]string, error) {
	if rawQuery == "" {
		return nil, nil
	}
	if p.Picker == nil || len(rawQuery) > maxSelectionQueryBytes {
		return nil, sep2admin.ErrInvalidSelection
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, sep2admin.ErrInvalidSelection
	}
	for k := range q {
		if k != "sel" {
			return nil, sep2admin.ErrInvalidSelection
		}
	}
	ids := q["sel"]
	if err := sep2admin.ValidateSelectionIDs(ids); err != nil {
		return nil, err
	}
	return ids, nil
}

// selectionView is the one guarded call a selection request makes:
// Choices, the match, then Select, or View when nothing matched.
func selectionView(p sep2admin.Panel, ids []string) sep2admin.ViewFunc {
	return func(ctx context.Context) (sep2admin.Descriptor, error) {
		choices, err := p.Picker.Choices(ctx)
		if err != nil {
			return sep2admin.Descriptor{}, err
		}
		sel, err := sep2admin.NewSelection(ids, choices)
		if err != nil {
			return sep2admin.Descriptor{}, err
		}
		if sel.Len() == 0 {
			return p.View(ctx)
		}
		return p.Picker.Select(ctx, sel)
	}
}

// handleGet answers GET /api/ui/panels/{id}[?sel=...] with the panel's
// Descriptor. Error bodies never carry the View's error text: a
// recovered panic embeds a stack with host paths, so the detail goes to
// the log only.
func (ps *panelSet) handleGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p, ok := ps.lookup(id)
		if !ok {
			writePanelError(w, http.StatusNotFound, "no such panel")
			return
		}
		ids, err := parseSelection(p, r.URL.RawQuery)
		if err != nil {
			writePanelError(w, http.StatusBadRequest, selectionRefusal)
			return
		}
		view := p.View
		if len(ids) > 0 {
			view = selectionView(p, ids)
		}
		d, ok := ps.invoke(w, r, p, view)
		if !ok {
			return
		}
		if d.Version != sep2admin.CurrentDescriptorVersion {
			log.Printf("admin: panel %q: descriptor version %d, want %d", id, d.Version, sep2admin.CurrentDescriptorVersion)
			writePanelError(w, http.StatusInternalServerError, "panel failed")
			return
		}
		if rows := d.RowCount(); rows > maxPanelRows {
			log.Printf("admin: panel %q: %d rows, over the cap of %d", id, rows, maxPanelRows)
			writePanelError(w, http.StatusInternalServerError, "panel response too large")
			return
		}
		// Encoded before any header is written, so a refused Descriptor
		// (an unsafe link, an undeclared badge) answers 500, not a
		// truncated 200.
		body, err := json.Marshal(d)
		if err != nil {
			log.Printf("admin: panel %q: encode: %v", id, err)
			writePanelError(w, http.StatusInternalServerError, "panel failed")
			return
		}
		if len(body) > maxPanelBytes {
			log.Printf("admin: panel %q: %d encoded bytes, over the cap of %d", id, len(body), maxPanelBytes)
			writePanelError(w, http.StatusInternalServerError, "panel response too large")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			log.Printf("admin: panel %q: write: %v", id, err)
		}
	}
}

// errPanelBusy is run's refusal when an earlier call still holds the
// panel's flag. The view was not started.
var errPanelBusy = errors.New("admin: panel is still answering an earlier request")

// run runs view for p under the panel's one-at-a-time flag and the plane's
// timeout. A returned error is errPanelBusy or one of InvokeView's.
func (ps *panelSet) run(r *http.Request, p sep2admin.Panel, view sep2admin.ViewFunc) (sep2admin.Descriptor, error) {
	// One View per panel at a time. A hung View keeps its goroutine
	// until it returns, so without this each read would leak one more.
	busy := ps.busy[p.ID]
	if !busy.CompareAndSwap(false, true) {
		return sep2admin.Descriptor{}, errPanelBusy
	}
	p.View = view
	guarded, settle := oneAtATime(p, busy)
	d, err := sep2admin.InvokeView(r.Context(), guarded, ps.timeout)
	settle()
	return d, err
}

// invoke is run, answering the failure itself and returning false.
func (ps *panelSet) invoke(w http.ResponseWriter, r *http.Request, p sep2admin.Panel, view sep2admin.ViewFunc) (sep2admin.Descriptor, bool) {
	d, err := ps.run(r, p, view)
	if err == nil {
		return d, true
	}
	status, msg := ps.failure(p, err)
	writePanelError(w, status, msg)
	return sep2admin.Descriptor{}, false
}

// failure maps a run error to its status and fixed body text, logging the
// detail. The text never carries the View's error.
func (ps *panelSet) failure(p sep2admin.Panel, err error) (int, string) {
	switch {
	case errors.Is(err, errPanelBusy):
		return http.StatusGatewayTimeout, "panel is still answering an earlier request"
	case errors.Is(err, sep2admin.ErrViewTimedOut), errors.Is(err, sep2admin.ErrViewNotInvoked):
		log.Printf("admin: panel %q: %v", p.ID, err)
		return http.StatusGatewayTimeout, "panel did not answer in time"
	case errors.Is(err, sep2admin.ErrViewCanceled):
		return http.StatusServiceUnavailable, "request canceled"
	default:
		log.Printf("admin: panel %q: %v", p.ID, err)
		return http.StatusInternalServerError, "panel failed"
	}
}

type choiceEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// handleChoices answers GET /api/ui/panels/{id}/choices with
// {"max":N,"choices":[{id,label}]}. The Choices call shares the panel's
// busy flag and timeout with View, and its list is validated before it is
// served.
func (ps *panelSet) handleChoices() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := ps.lookup(r.PathValue("id"))
		if !ok || p.Picker == nil {
			writePanelError(w, http.StatusNotFound, "no such panel")
			return
		}
		var choices []sep2admin.Choice
		_, ok = ps.invoke(w, r, p, func(ctx context.Context) (sep2admin.Descriptor, error) {
			c, err := p.Picker.Choices(ctx)
			if err != nil {
				return sep2admin.Descriptor{}, err
			}
			if err := sep2admin.ValidateChoices(c); err != nil {
				return sep2admin.Descriptor{}, err
			}
			choices = c
			return sep2admin.Descriptor{}, nil
		})
		if !ok {
			return
		}
		out := struct {
			Max     int           `json:"max"`
			Choices []choiceEntry `json:"choices"`
		}{Max: sep2admin.MaxSelection, Choices: make([]choiceEntry, 0, len(choices))}
		for _, c := range choices {
			out.Choices = append(out.Choices, choiceEntry{ID: c.ID, Label: c.Label})
		}
		writePanelJSON(w, http.StatusOK, out)
	}
}

// oneAtATime returns p with a View that frees busy when the real View
// returns, which can be long after InvokeView gave up on it, and a settle
// func the caller runs once InvokeView returns. InvokeView may never start
// the View (the request was already gone); whichever of the View and
// settle claims started first decides, so busy is freed exactly once and
// a View that starts after settle does not run.
func oneAtATime(p sep2admin.Panel, busy *atomic.Bool) (sep2admin.Panel, func()) {
	var started atomic.Bool
	view := p.View
	p.View = func(ctx context.Context) (sep2admin.Descriptor, error) {
		if !started.CompareAndSwap(false, true) {
			return sep2admin.Descriptor{}, context.Canceled
		}
		defer busy.Store(false)
		return view(ctx)
	}
	settle := func() {
		if started.CompareAndSwap(false, true) {
			busy.Store(false)
		}
	}
	return p, settle
}

func (ps *panelSet) lookup(id string) (sep2admin.Panel, bool) {
	for _, p := range ps.panels {
		if p.ID == id {
			return p, true
		}
	}
	return sep2admin.Panel{}, false
}

func writePanelError(w http.ResponseWriter, status int, msg string) {
	writePanelJSON(w, status, struct {
		Error string `json:"error"`
	}{msg})
}

func writePanelJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		log.Printf("admin: panels: encode: %v", err)
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(body); err != nil {
		log.Printf("admin: panels: write: %v", err)
	}
}
