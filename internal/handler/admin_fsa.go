package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #163: admin FSA hierarchy management.
//
// Endpoint matrix (all behind AdminAuthMiddleware via the admin router):
//
//	POST   /api/fsas                              create
//	GET    /api/fsas                              list
//	GET    /api/fsas/{id}                         single
//	DELETE /api/fsas/{id}                         delete (404/409)
//	POST   /api/fsas/{id}/programs                attach DERProgram by href
//	POST   /api/fsas/{id}/der-programs            create a program template (admin_derprogram.go)
//	GET    /api/fsas/{id}/der-programs            list program templates
//	PUT    /api/fsas/{id}/der-programs/{derpId}   edit a template and its copies (admin_derprogram_edit.go)
//	DELETE /api/fsas/{id}/der-programs/{derpId}   delete a template and its copies
//	DELETE /api/fsas/{id}/programs?href=<enc>     detach
//	POST   /api/devices/{id}/fsa-assignment       assign device to FSA
//	DELETE /api/devices/{id}/fsa-assignment?fsaHref=<enc>  unassign (409 while device programs sit under it)
//	POST   /api/devices/{id}/fsas/{fsaId}/der-programs     create a device program (admin_derprogram.go)
//	PUT    /api/devices/{id}/der-programs/{derpId}         edit a device or fixture program
//	DELETE /api/devices/{id}/der-programs/{derpId}         delete a device or fixture program
//	GET    /api/topology                          SY -> FD -> SP -> DEV tree
//
// Two-store split: the management plane lives in *memory.AdminFSAStore.
// Per-device scoped FSAs (*memory.ScopedStore[FunctionSetAssignments]) are
// still the spec-facing surface; the assignment endpoint materializes the
// admin FSA into the device's scoped store so /edev/{id}/fsa walks see it.

// AdminFSAHandler is the dependency surface for the admin FSA endpoints.
// Interfaces are kept small and defined here at the consumer per the Pike
// rule. Production wiring passes concrete types; tests use stubs.
type AdminFSAHandler struct {
	AdminFSAs   *memory.AdminFSAStore
	DeviceFSAs  DeviceFSAWriter
	EndDevices  EndDeviceReader
	DERPrograms DERProgramHrefValidator

	// Copies are the stores a program template is copied into. Nil leaves
	// the template routes unmounted, and an assignment to an FSA that has
	// templates answers 500.
	Copies *fsaprogram.Copies
	// Notifier is told of every program list and FSA list a template create
	// or an assignment changes. Nil sends nothing.
	Notifier ResourceNotifier
	// PEN is the low 32 bits of every minted mRID; nil mints all 128 bits.
	PEN *uint32
	// Logger receives the template and copy write log lines; nil uses
	// slog's default.
	Logger *slog.Logger
	// Retirer gates every program delete on its control scopes. Nil
	// answers every delete 500.
	Retirer ProgramRetirer

	// programMu serializes every program write with assignments, so a device
	// assigned while a template is created gets exactly one copy of it.
	programMu sync.Mutex
}

// DeviceFSAWriter mirrors the slice of *memory.ScopedStore[FSA] surface
// used to materialize an admin FSA into a device's scoped store.
type DeviceFSAWriter interface {
	Get(ctx context.Context, parentID, id string) (sep2.FunctionSetAssignments, error)
	Create(ctx context.Context, parentID, id string, resource sep2.FunctionSetAssignments) error
	Delete(ctx context.Context, parentID, id string) error
}

// EndDeviceReader is the read surface needed to confirm a device id exists
// before assignment.
type EndDeviceReader interface {
	Get(ctx context.Context, id string) (sep2.EndDevice, error)
}

// DERProgramHrefValidator confirms that a given DERProgram href resolves
// to an existing program. Implementation walks the program store; in tests
// a stub returns canned answers.
type DERProgramHrefValidator interface {
	HasProgram(ctx context.Context, href string) bool
}

// fsaCreateRequest is the body for POST /api/fsas.
type fsaCreateRequest struct {
	Description string `json:"description"`
	Primacy     uint8  `json:"primacy"`
	MRID        string `json:"mRID"`
}

// fsaResponse is the shape returned by create/get.
type fsaResponse struct {
	Href        string   `json:"href"`
	MRID        string   `json:"mRID"`
	Description string   `json:"description"`
	Primacy     uint8    `json:"primacy"`
	Programs    []string `json:"programs,omitempty"`
	Devices     []string `json:"devices,omitempty"`
	// DeviceOnly names the one device a device-only FSA is bound to.
	DeviceOnly string `json:"deviceOnly,omitempty"`
}

// HandleCreateAdminFSA returns a handler for POST /api/fsas.
func (h *AdminFSAHandler) HandleCreateAdminFSA() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req fsaCreateRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if strings.TrimSpace(req.Description) == "" {
			writeError(w, http.StatusBadRequest, "description required")
			return
		}

		id := strings.TrimSpace(req.MRID)
		if id == "" {
			id = newFSAID()
		}
		fsa := sep2.FunctionSetAssignments{
			Resource:           sep2.Resource{Href: "/api/fsas/" + id},
			MRID:               id,
			Description:        req.Description,
			DERProgramListLink: &sep2.ListLink{Href: "/api/fsas/" + id + "/programs"},
		}

		if err := h.AdminFSAs.Create(r.Context(), id, fsa); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				writeError(w, http.StatusConflict, "fsa with this mRID already exists")
				return
			}
			writeError(w, http.StatusInternalServerError, "create fsa: "+err.Error())
			return
		}

		w.Header().Set("Location", fsa.Href)
		writeJSON(w, http.StatusCreated, fsaResponse{
			Href:        fsa.Href,
			MRID:        id,
			Description: req.Description,
			Primacy:     req.Primacy,
		})
	}
}

// HandleListAdminFSAs returns a handler for GET /api/fsas.
func (h *AdminFSAHandler) HandleListAdminFSAs() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		list := h.AdminFSAs.List(r.Context())
		out := make([]fsaResponse, 0, len(list))
		for _, f := range list {
			out = append(out, fsaResponse{
				Href:        f.Href,
				MRID:        f.MRID,
				Description: f.Description,
				Programs:    h.AdminFSAs.Programs(r.Context(), f.MRID),
				Devices:     h.AdminFSAs.Devices(r.Context(), f.MRID),
				DeviceOnly:  h.AdminFSAs.DeviceOnlyDevice(f.MRID),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"fsas": out})
	}
}

// HandleGetAdminFSA returns a handler for GET /api/fsas/{id}.
func (h *AdminFSAHandler) HandleGetAdminFSA() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "id required")
			return
		}
		fsa, err := h.AdminFSAs.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "fsa not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "get fsa: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, fsaResponse{
			Href:        fsa.Href,
			MRID:        fsa.MRID,
			Description: fsa.Description,
			Programs:    h.AdminFSAs.Programs(r.Context(), id),
			Devices:     h.AdminFSAs.Devices(r.Context(), id),
			DeviceOnly:  h.AdminFSAs.DeviceOnlyDevice(id),
		})
	}
}

// HandleDeleteAdminFSA returns a handler for DELETE /api/fsas/{id}.
// 204 on success, 404 if absent, 409 if FSA still has programs/devices.
func (h *AdminFSAHandler) HandleDeleteAdminFSA() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "id required")
			return
		}
		err := h.AdminFSAs.Delete(r.Context(), id)
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "fsa not found")
		case errors.Is(err, memory.ErrAdminFSAInUse):
			writeError(w, http.StatusConflict, "fsa still has programs or devices attached")
		default:
			writeError(w, http.StatusInternalServerError, "delete fsa: "+err.Error())
		}
	}
}

// programRequest is the body for POST /api/fsas/{id}/programs.
type programRequest struct {
	ProgramHref string `json:"programHref"`
}

// HandleAttachProgram returns a handler for POST /api/fsas/{id}/programs.
func (h *AdminFSAHandler) HandleAttachProgram() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "id required")
			return
		}

		var req programRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if strings.TrimSpace(req.ProgramHref) == "" {
			writeError(w, http.StatusBadRequest, "programHref required")
			return
		}

		// Validate href resolves to an existing DERProgram.
		if h.DERPrograms != nil && !h.DERPrograms.HasProgram(r.Context(), req.ProgramHref) {
			writeError(w, http.StatusNotFound, "DERProgram not found at "+req.ProgramHref)
			return
		}

		err := h.AdminFSAs.AttachProgram(r.Context(), id, req.ProgramHref)
		switch {
		case err == nil:
			writeJSON(w, http.StatusCreated, map[string]string{
				"fsa":         id,
				"programHref": req.ProgramHref,
			})
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "fsa not found")
		case errors.Is(err, store.ErrAlreadyExists):
			writeError(w, http.StatusConflict, "program already attached")
		default:
			writeError(w, http.StatusInternalServerError, "attach: "+err.Error())
		}
	}
}

// HandleDetachProgram returns a handler for DELETE /api/fsas/{id}/programs?href=...
func (h *AdminFSAHandler) HandleDetachProgram() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "id required")
			return
		}
		href := r.URL.Query().Get("href")
		if strings.TrimSpace(href) == "" {
			writeError(w, http.StatusBadRequest, "href query param required")
			return
		}

		err := h.AdminFSAs.DetachProgram(r.Context(), id, href)
		switch {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "fsa or program link not found")
		default:
			writeError(w, http.StatusInternalServerError, "detach: "+err.Error())
		}
	}
}

// assignmentRequest is the body for POST /api/devices/{id}/fsa-assignment.
type assignmentRequest struct {
	FSAHref string `json:"fsaHref"`
}

// HandleAssignDeviceFSA returns a handler for POST /api/devices/{id}/fsa-assignment.
// Materializes the admin FSA into the device's scoped FSA store so the
// per-device /edev/{id}/fsa walks see it. 404 if device or admin FSA missing;
// 409 if already assigned.
func (h *AdminFSAHandler) HandleAssignDeviceFSA() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceID := r.PathValue("id")
		if deviceID == "" {
			writeError(w, http.StatusBadRequest, "id required")
			return
		}

		var req assignmentRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		fsaID := extractFSAID(req.FSAHref)
		if fsaID == "" {
			writeError(w, http.StatusBadRequest, "fsaHref must be /api/fsas/{id}")
			return
		}

		// Confirm device exists.
		if h.EndDevices != nil {
			if _, err := h.EndDevices.Get(r.Context(), deviceID); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeError(w, http.StatusNotFound, "device not found")
					return
				}
				writeError(w, http.StatusInternalServerError, "device lookup: "+err.Error())
				return
			}
		}

		// Confirm admin FSA exists.
		fsa, err := h.AdminFSAs.Get(r.Context(), fsaID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "admin FSA not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "fsa lookup: "+err.Error())
			return
		}

		h.programMu.Lock()
		defer h.programMu.Unlock()

		// Every template copy is checked before anything is written, so a
		// conflict refuses the assignment with nothing to undo.
		tmpls := h.AdminFSAs.Templates(r.Context(), fsaID)
		if len(tmpls) > 0 && h.Copies == nil {
			h.templateFailed(w, r, "no_program_stores", errors.New("program stores are not wired"))
			return
		}
		for _, t := range tmpls {
			if _, err := h.Copies.Check(r.Context(), deviceID, fsaID, t); err != nil {
				if errors.Is(err, fsaprogram.ErrCopyConflict) {
					writeError(w, http.StatusConflict, "device already holds a program with the id or mRID of one of this fsa's templates")
					return
				}
				h.templateFailed(w, r, "copy_check", err)
				return
			}
		}

		// Record the assignment.
		if err := h.AdminFSAs.AssignDevice(r.Context(), fsaID, deviceID); err != nil {
			if errors.Is(err, memory.ErrAdminFSADeviceOnly) {
				h.deviceOnlyRefused(w, fsaID)
				return
			}
			if errors.Is(err, store.ErrAlreadyExists) {
				writeError(w, http.StatusConflict, "device already assigned to this FSA")
				return
			}
			writeError(w, http.StatusInternalServerError, "assign: "+err.Error())
			return
		}

		// Materialize into the device-scoped store so /edev/{id}/fsa lists it.
		// The scoped href differs from the admin href: the spec surface lives
		// under the device's edev tree.
		// The assignment is undone last, so a write whose undo fails still
		// has its assignment, and the boot rebuild completes it.
		steps := []rollbackStep{{device: deviceID, part: "assignment", undo: func(ctx context.Context) error {
			return h.AdminFSAs.UnassignDevice(ctx, fsaID, deviceID)
		}}}
		if h.DeviceFSAs != nil {
			scoped := fsaprogram.Scoped(deviceID, fsaID, fsa)
			switch err := h.DeviceFSAs.Create(r.Context(), deviceID, fsaID, scoped); {
			case err == nil:
				steps = append(steps, rollbackStep{device: deviceID, part: "fsa", undo: func(ctx context.Context) error {
					return h.DeviceFSAs.Delete(ctx, deviceID, fsaID)
				}})
			case errors.Is(err, store.ErrAlreadyExists):
			default:
				if !h.rollbackAfter(r, err, steps) {
					h.assignIncomplete(w, deviceID, fsaID)
					return
				}
				writeError(w, http.StatusInternalServerError, "scope materialize: "+err.Error())
				return
			}
		}
		for _, t := range tmpls {
			undo, err := h.Copies.Ensure(r.Context(), deviceID, fsaID, t)
			if err == nil {
				steps = append(steps, rollbackStep{device: deviceID, template: t.ID, part: "copy", undo: undo})
				continue
			}
			if !h.rollbackAfter(r, err, steps) {
				h.assignIncomplete(w, deviceID, fsaID)
				return
			}
			h.copyRefused(w, r, err)
			return
		}

		h.notify(r.Context(), "/edev/"+deviceID+"/fsa")
		h.notify(r.Context(), programListHref(deviceID, fsaID))

		writeJSON(w, http.StatusOK, map[string]string{
			"device":  deviceID,
			"fsaHref": req.FSAHref,
		})
	}
}

// HandleUnassignDeviceFSA returns a handler for DELETE /api/devices/{id}/fsa-assignment?fsaHref=...
func (h *AdminFSAHandler) HandleUnassignDeviceFSA() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		deviceID := r.PathValue("id")
		if deviceID == "" {
			writeError(w, http.StatusBadRequest, "id required")
			return
		}
		fsaHref := r.URL.Query().Get("fsaHref")
		fsaID := extractFSAID(fsaHref)
		if fsaID == "" {
			writeError(w, http.StatusBadRequest, "fsaHref query param required")
			return
		}

		h.programMu.Lock()
		defer h.programMu.Unlock()

		if !slices.Contains(h.AdminFSAs.Devices(r.Context(), fsaID), deviceID) {
			writeError(w, http.StatusNotFound, "assignment not found")
			return
		}

		// A device program is the operator's own, so unlike a template copy
		// it is never removed on the operator's behalf (#921).
		if h.Copies != nil {
			held, err := h.Copies.DevicePrograms(r.Context(), deviceID, fsaID)
			if err != nil {
				h.templateFailed(w, r, "device_programs", err)
				return
			}
			if len(held) > 0 {
				writeJSON(w, http.StatusConflict, deviceProgramsPresent{
					Error:    "device programs sit under this fsa and must be deleted before the device is unassigned: " + strings.Join(held, ", "),
					Code:     "device_programs_present",
					Programs: held,
				})
				return
			}
		}

		// Copies go before the assignment, so a removal that fails leaves
		// the device assigned and the boot rebuild restores its copies.
		tmpls := h.AdminFSAs.Templates(r.Context(), fsaID)
		if len(tmpls) > 0 && h.Copies == nil {
			h.templateFailed(w, r, "no_program_stores", errors.New("program stores are not wired"))
			return
		}
		var removed []string
		notifyRemoved := func() {
			for _, href := range removed {
				h.notifyStatus(r.Context(), href, sep2.NotificationStatusCanceledDeleted)
			}
			if len(removed) > 0 {
				h.notify(r.Context(), programListHref(deviceID, fsaID))
			}
		}
		for _, t := range tmpls {
			ok, err := h.Copies.Remove(r.Context(), deviceID, fsaID, t)
			if err != nil {
				var part *fsaprogram.PartError
				if errors.As(err, &part) {
					h.logUndoFailed(r, part.DeviceID, part.TemplateID, part.Part, err)
				} else {
					h.logUndoFailed(r, deviceID, t.ID, "copy", err)
				}
				notifyRemoved()
				writeError(w, http.StatusInternalServerError, fmt.Sprintf(
					"unassign failed; device %s stays assigned to fsa %s, and its template copies are restored at the next restart", deviceID, fsaID))
				return
			}
			if ok {
				removed = append(removed, der.DERProgramHref(deviceID, fsaID, t.ID))
			}
		}

		err := h.AdminFSAs.UnassignDevice(r.Context(), fsaID, deviceID)
		notifyRemoved()
		switch {
		case err == nil:
			if h.DeviceFSAs != nil {
				if delErr := h.DeviceFSAs.Delete(r.Context(), deviceID, fsaID); delErr != nil && !errors.Is(delErr, store.ErrNotFound) {
					// The assignment is gone; the in-memory FSA goes at the next restart.
					h.logged(r).ErrorContext(r.Context(), "admin: device FSA left after unassign", "event", "device_fsa_delete_failed",
						"device", deviceID, "fsa", fsaID, "error", delErr)
				}
			}
			h.notify(r.Context(), "/edev/"+deviceID+"/fsa")
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "assignment not found")
		default:
			writeError(w, http.StatusInternalServerError, "unassign: "+err.Error())
		}
	}
}

// deviceProgramsPresent is the 409 body of an unassign refused by device
// programs; Programs lists their ids.
type deviceProgramsPresent struct {
	Error    string   `json:"error"`
	Code     string   `json:"code"`
	Programs []string `json:"programs"`
}

// assignIncomplete answers an assignment whose rollback stopped partway: the
// device stays assigned, and the boot rebuild completes its FSA and copies.
func (h *AdminFSAHandler) assignIncomplete(w http.ResponseWriter, deviceID, fsaID string) {
	writeError(w, http.StatusInternalServerError, fmt.Sprintf(
		"assign failed; device %s stays assigned to fsa %s, and its template copies are completed at the next restart", deviceID, fsaID))
}

// extractFSAID parses /api/fsas/{id} into id, or returns "".
func extractFSAID(href string) string {
	const prefix = "/api/fsas/"
	href = strings.TrimSpace(href)
	if !strings.HasPrefix(href, prefix) {
		return ""
	}
	tail := strings.TrimPrefix(href, prefix)
	// Reject empty, slashes, query strings.
	if tail == "" || strings.ContainsAny(tail, "/?#") {
		return ""
	}
	return tail
}

// newFSAID generates a short hex id when the caller doesn't supply mRID.
func newFSAID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "fsa-" + hex.EncodeToString(b[:])
}
