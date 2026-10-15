package handler

import (
	"cmp"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #923: every DER program on the server.
//
//	GET /api/der-programs?limit=<n>&start=<n>

const (
	// Origin values: the index records template and device; a program with
	// no index entry came from the boot fixture (memory.ProgramOrigin).
	programOriginFixture = "fixture"

	// OnRestart values.
	onRestartKept     = "kept"
	onRestartReseeded = "reseeded"
	onRestartGone     = "gone"

	defaultProgramPage = 100
	maxProgramPage     = 500

	programListInternalError = "program list failed"
)

// ProgramLister is the read surface of the DERProgram store, keyed
// (device id, program id).
type ProgramLister interface {
	Parents(ctx context.Context) ([]string, error)
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERProgram], error)
}

// ProgramOriginReader is the read surface of the origin index.
type ProgramOriginReader interface {
	Get(ctx context.Context, deviceID, programID string) (memory.ProgramOrigin, error)
}

// Persister is implemented by a store that knows whether it writes a
// snapshot. A nil Persister counts as in-memory.
type Persister interface{ Persists() bool }

func persists(p Persister) bool { return p != nil && p.Persists() }

// onRestart is what the program record does across a restart. Only the
// DERProgram store decides it: a program written to its snapshot is loaded
// again at boot, and nothing at boot deletes a program for lack of an origin
// entry or a template (fsaprogram.ReconcileCopies skips a copy it cannot
// rebuild and removes only a device origin whose program is missing). So an
// unpersisted origin index or template store changes how a program is listed
// after the restart, never whether it survives.
//
//   - template and device programs: kept with a data dir, gone without.
//   - fixture: with a data dir the fixture seeds each program once and yields
//     to the stored one (bootfixture.Reconcile, "keep, or adopt if
//     unseeded"), so an operator edit wins; without one, bootfixture.Load
//     creates every fixture program again.
//
// A missing snapshot at boot is the exception (#950): it is not modeled here.
func onRestart(programs Persister, origin string) string {
	if persists(programs) {
		return onRestartKept
	}
	if origin == programOriginFixture {
		return onRestartReseeded
	}
	return onRestartGone
}

// AdminProgramListHandler serves GET /api/der-programs.
type AdminProgramListHandler struct {
	Programs ProgramLister
	Origins  ProgramOriginReader
	// Persist is the DERProgram store; nil counts as in-memory.
	Persist Persister
	// Logger receives read failures; nil uses slog's default.
	Logger *slog.Logger
}

// ProgramListItem is one program as GET /api/der-programs reports it.
// TemplateID is set for a template copy only.
type ProgramListItem struct {
	Device      string `json:"device"`
	FSA         string `json:"fsa"`
	ID          string `json:"id"`
	Href        string `json:"href"`
	MRID        string `json:"mRID"`
	Description string `json:"description"`
	Primacy     uint8  `json:"primacy"`
	Version     uint16 `json:"version"`
	Origin      string `json:"origin"`
	TemplateID  string `json:"templateId,omitempty"`
	OnRestart   string `json:"onRestart"`
}

// ProgramListReply is the whole body. Persists says whether programs are
// written to disk at all; Total counts every program, Start and Limit echo
// the page taken. Programs is [] and never null.
type ProgramListReply struct {
	Persists bool              `json:"persists"`
	Total    int               `json:"total"`
	Start    int               `json:"start"`
	Limit    int               `json:"limit"`
	Programs []ProgramListItem `json:"programs"`
}

type listedKey struct{ device, href, id string }

// HandleList returns the handler for GET /api/der-programs. Programs are
// ordered by device id, then href, so a page is stable between calls.
func (h *AdminProgramListHandler) HandleList() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start, limit, msg := parseProgramPage(r)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		ctx := r.Context()
		progs := map[listedKey]sep2.DERProgram{}
		parents, err := h.Programs.Parents(ctx)
		if err != nil {
			h.failed(w, r, "parents", "", "", err)
			return
		}
		keys := []listedKey{}
		for _, dev := range parents {
			page, err := h.Programs.List(ctx, dev, store.ListOptions{Unbounded: true})
			if err != nil {
				h.failed(w, r, "list", dev, "", err)
				return
			}
			for _, p := range page.Items {
				k := listedKey{device: dev, href: p.Href, id: programKey(p.Href)}
				keys = append(keys, k)
				progs[k] = p
			}
		}
		slices.SortFunc(keys, func(a, b listedKey) int {
			if c := cmp.Compare(a.device, b.device); c != 0 {
				return c
			}
			return cmp.Compare(a.href, b.href)
		})

		reply := ProgramListReply{Persists: persists(h.Persist), Total: len(keys), Start: start, Limit: limit, Programs: []ProgramListItem{}}
		if start < len(keys) {
			end := min(start+limit, len(keys))
			for _, k := range keys[start:end] {
				item, err := h.item(ctx, k, progs[k])
				if err != nil {
					h.failed(w, r, "origin", k.device, k.id, err)
					return
				}
				reply.Programs = append(reply.Programs, item)
			}
		}
		writeJSON(w, http.StatusOK, reply)
	}
}

func (h *AdminProgramListHandler) item(ctx context.Context, k listedKey, p sep2.DERProgram) (ProgramListItem, error) {
	_, fsa, _, ok := derhref.Program(p.Href)
	if !ok {
		h.logger().WarnContext(ctx, "admin: DER program href is not /edev/{id}/fsa/{fsaId}/derp/{id}; listing it with no fsa",
			"event", "der_program_list_bad_href", "device", k.device, "program", k.id)
	}
	item := ProgramListItem{
		Device: k.device, FSA: fsa, ID: k.id, Href: p.Href, MRID: p.MRID,
		Description: p.Description, Primacy: p.Primacy, Origin: programOriginFixture,
	}
	if p.Version != nil {
		item.Version = *p.Version
	}
	o, err := h.Origins.Get(ctx, k.device, k.id)
	switch {
	case err == nil:
		item.Origin = string(o.Kind)
		item.TemplateID = o.TemplateID
	case errors.Is(err, store.ErrNotFound):
	default:
		return item, err
	}
	item.OnRestart = onRestart(h.Persist, item.Origin)
	return item, nil
}

// programKey is the program id, the last segment of its href. The store key
// of every program is that segment.
func programKey(href string) string {
	return href[strings.LastIndex(href, "/")+1:]
}

// parseProgramPage reads limit and start, or returns the 400 message. limit
// defaults to defaultProgramPage and is capped at maxProgramPage, and the
// reply echoes the value used.
func parseProgramPage(r *http.Request) (start, limit int, msg string) {
	q := r.URL.Query()
	limit = defaultProgramPage
	if vs, ok := q["limit"]; ok {
		n, err := strconv.Atoi(vs[0])
		if len(vs) != 1 || err != nil || n < 1 {
			return 0, 0, "limit: a positive integer is required"
		}
		limit = min(n, maxProgramPage)
	}
	if vs, ok := q["start"]; ok {
		n, err := strconv.Atoi(vs[0])
		if len(vs) != 1 || err != nil || n < 0 {
			return 0, 0, "start: a non-negative integer is required"
		}
		start = n
	}
	return start, limit, ""
}

func (h *AdminProgramListHandler) logger() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// failed logs the cause, with the device and program it failed on when known,
// and answers a fixed 500.
func (h *AdminProgramListHandler) failed(w http.ResponseWriter, r *http.Request, step, device, program string, err error) {
	h.logger().ErrorContext(r.Context(), "admin: DER program list failed", "event", "der_program_list_failed",
		"step", step, "device", device, "program", program, "path", r.URL.Path, "error", err)
	writeError(w, http.StatusInternalServerError, programListInternalError)
}
