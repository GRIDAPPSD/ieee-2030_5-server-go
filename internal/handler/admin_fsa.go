package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
)

// IEEE-096: admin FSA hierarchy management.
//
// Endpoint matrix (all behind AdminAuthMiddleware via the admin router):
//
//	POST   /api/fsas                              create
//	GET    /api/fsas                              list
//	GET    /api/fsas/{id}                         single
//	DELETE /api/fsas/{id}                         delete (404/409)
//	POST   /api/fsas/{id}/programs                attach DERProgram by href
//	DELETE /api/fsas/{id}/programs?href=<enc>     detach
//	POST   /api/devices/{id}/fsa-assignment       assign device to FSA
//	DELETE /api/devices/{id}/fsa-assignment?fsaHref=<enc>  unassign
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
	AdminFSAs     *memory.AdminFSAStore
	DeviceFSAs    DeviceFSAWriter
	EndDevices    EndDeviceReader
	DERPrograms   DERProgramHrefValidator
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
}

// HandleCreateAdminFSA returns a handler for POST /api/fsas.
func (h *AdminFSAHandler) HandleCreateAdminFSA() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req fsaCreateRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
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
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
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
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
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

		// Record the assignment.
		if err := h.AdminFSAs.AssignDevice(r.Context(), fsaID, deviceID); err != nil {
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
		if h.DeviceFSAs != nil {
			scopedHref := fmt.Sprintf("/edev/%s/fsa/%s", deviceID, fsaID)
			scoped := sep2.FunctionSetAssignments{
				Resource:           sep2.Resource{Href: scopedHref},
				MRID:               fsa.MRID,
				Description:        fsa.Description,
				DERProgramListLink: &sep2.ListLink{Href: scopedHref + "/derp"},
			}
			if err := h.DeviceFSAs.Create(r.Context(), deviceID, fsaID, scoped); err != nil && !errors.Is(err, store.ErrAlreadyExists) {
				// Best-effort: undo the link record if we can't persist.
				_ = h.AdminFSAs.UnassignDevice(r.Context(), fsaID, deviceID)
				writeError(w, http.StatusInternalServerError, "scope materialize: "+err.Error())
				return
			}
		}

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

		err := h.AdminFSAs.UnassignDevice(r.Context(), fsaID, deviceID)
		switch {
		case err == nil:
			// Best-effort cleanup of the scoped materialization.
			if h.DeviceFSAs != nil {
				if delErr := h.DeviceFSAs.Delete(r.Context(), deviceID, fsaID); delErr != nil && !errors.Is(delErr, store.ErrNotFound) {
					// Logging only — the link record is already gone.
					_ = delErr
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, store.ErrNotFound):
			writeError(w, http.StatusNotFound, "assignment not found")
		default:
			writeError(w, http.StatusInternalServerError, "unassign: "+err.Error())
		}
	}
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
