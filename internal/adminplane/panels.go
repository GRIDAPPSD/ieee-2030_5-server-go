package adminplane

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

// panelViewTimeout bounds one View call. A View still running at the
// deadline answers 504; its goroutine is not killed (see InvokeView).
const panelViewTimeout = 5 * time.Second

// panelSet is the frozen extension band an embedder registered: the
// panels the shell adds after its own tabs. The core band holds the
// shell's tabs, which the shell renders itself, so it is never served.
type panelSet struct {
	panels  []sep2admin.Panel
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
		}
	}
	return ps, nil
}

// noPanels is the set BuildAdminRouter mounts: the routes exist and list
// nothing.
func noPanels() *panelSet {
	return &panelSet{panels: []sep2admin.Panel{}, timeout: panelViewTimeout}
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
		d, err := sep2admin.InvokeView(r.Context(), p, ps.timeout)
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
		// Encoded before any header is written, so a refused Descriptor
		// (an unsafe link, an undeclared badge) answers 500, not a
		// truncated 200.
		body, err := json.Marshal(d)
		if err != nil {
			log.Printf("admin: panel %q: encode: %v", id, err)
			writePanelError(w, http.StatusInternalServerError, "panel failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(body); err != nil {
			log.Printf("admin: panel %q: write: %v", id, err)
		}
	}
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
