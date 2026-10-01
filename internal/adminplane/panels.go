package adminplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
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
	panels  []sep2admin.Panel
	busy    map[string]*atomic.Bool
	timeout time.Duration
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
	return &panelSet{panels: []sep2admin.Panel{}, busy: map[string]*atomic.Bool{}, timeout: panelViewTimeout}
}

type panelEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// handleList answers GET /api/ui/panels with [{id, label}] in frozen
// order, and [] when nothing is registered.
func (ps *panelSet) handleList() http.HandlerFunc {
	entries := make([]panelEntry, 0, len(ps.panels))
	for _, p := range ps.panels {
		entries = append(entries, panelEntry{ID: p.ID, Label: p.Label})
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		writePanelJSON(w, http.StatusOK, entries)
	}
}

// handleGet answers GET /api/ui/panels/{id} with the panel's Descriptor.
// Error bodies never carry the View's error text: a recovered panic
// embeds a stack with host paths, so the detail goes to the log only.
func (ps *panelSet) handleGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p, ok := ps.lookup(id)
		if !ok {
			writePanelError(w, http.StatusNotFound, "no such panel")
			return
		}
		// One View per panel at a time. A hung View keeps its goroutine
		// until it returns, so without this each read would leak one more.
		busy := ps.busy[id]
		if !busy.CompareAndSwap(false, true) {
			writePanelError(w, http.StatusGatewayTimeout, "panel is still answering an earlier request")
			return
		}
		guarded, settle := oneAtATime(p, busy)
		d, err := sep2admin.InvokeView(r.Context(), guarded, ps.timeout)
		settle()
		switch {
		case err == nil:
		case errors.Is(err, sep2admin.ErrViewTimedOut), errors.Is(err, sep2admin.ErrViewNotInvoked):
			log.Printf("admin: panel %q: %v", id, err)
			writePanelError(w, http.StatusGatewayTimeout, "panel did not answer in time")
			return
		case errors.Is(err, sep2admin.ErrViewCanceled):
			writePanelError(w, http.StatusServiceUnavailable, "request canceled")
			return
		default:
			log.Printf("admin: panel %q: %v", id, err)
			writePanelError(w, http.StatusInternalServerError, "panel failed")
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
