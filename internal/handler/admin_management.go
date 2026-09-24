package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// #440: admin-plane provisioning of EndDevice management pairs.
//
// Endpoint matrix (all behind AdminAuthMiddleware via the admin router):
//
//	POST   /api/management-pairs         create (manager, managed)
//	GET    /api/management-pairs         list, by ?manager= or ?managed=
//	DELETE /api/management-pairs         remove, by ?managed=
//	POST   /api/management-pairs/rekey   replace a manager or managed LFDI
//
// The store already decides protocol access from these pairs
// (pkg/sep2srv/assembly/ownership.go); this file is the only way an
// operator can put one in. See docs/enddevice-access.md for the access
// model these pairs grant.

// AdminManagementHandler is the dependency surface for the management-pair
// admin endpoints. Managers is the concrete store, not the narrower
// store.EndDeviceManagementStore interface, because RekeyManager and
// RekeyManaged are not part of that interface: the interface stays the
// minimal contract every implementation must satisfy, and a
// provisioning-plane operation like re-key is not one of them.
type AdminManagementHandler struct {
	Managers *memory.EndDeviceManagementStore
}

// managementPairRequest is the body for POST /api/management-pairs.
type managementPairRequest struct {
	ManagerLFDI string `json:"managerLFDI"`
	ManagedLFDI string `json:"managedLFDI"`
}

// managementPairResponse is the shape returned by create and by a
// list-by-managed-device lookup.
type managementPairResponse struct {
	ManagerLFDI string `json:"managerLFDI"`
	ManagedLFDI string `json:"managedLFDI"`
}

// HandleCreateManagementPair returns a handler for POST /api/management-pairs.
// Idempotent: assigning the same pair again still answers 201. A device
// already managed by someone else answers 409; an empty LFDI or a device
// named as its own manager answers 400.
func (h *AdminManagementHandler) HandleCreateManagementPair() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req managementPairRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}

		managerLFDI, err := normalizeLFDI(req.ManagerLFDI)
		if err != nil {
			writeError(w, http.StatusBadRequest, "managerLFDI: "+err.Error())
			return
		}
		managedLFDI, err := normalizeLFDI(req.ManagedLFDI)
		if err != nil {
			writeError(w, http.StatusBadRequest, "managedLFDI: "+err.Error())
			return
		}
		if managerLFDI == managedLFDI {
			writeError(w, http.StatusBadRequest, "a device cannot manage itself")
			return
		}

		if err := h.Managers.Assign(r.Context(), managerLFDI, managedLFDI); err != nil {
			writeManagementPairError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, managementPairResponse{ManagerLFDI: managerLFDI, ManagedLFDI: managedLFDI})
	}
}

// HandleListManagementPairs returns a handler for GET /api/management-pairs.
// Exactly one of "manager" or "managed" selects the direction: by manager
// lists every device it manages (possibly empty), by managed device returns
// its single manager (404 if unmanaged).
func (h *AdminManagementHandler) HandleListManagementPairs() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		managerQ := strings.TrimSpace(r.URL.Query().Get("manager"))
		managedQ := strings.TrimSpace(r.URL.Query().Get("managed"))

		switch {
		case managerQ != "" && managedQ != "":
			writeError(w, http.StatusBadRequest, "manager and managed are mutually exclusive")
		case managerQ != "":
			manager, err := normalizeLFDI(managerQ)
			if err != nil {
				writeError(w, http.StatusBadRequest, "manager: "+err.Error())
				return
			}
			managed, err := h.Managers.ManagedBy(r.Context(), manager)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "list: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"managerLFDI":  manager,
				"managedLFDIs": managed,
			})
		case managedQ != "":
			managed, err := normalizeLFDI(managedQ)
			if err != nil {
				writeError(w, http.StatusBadRequest, "managed: "+err.Error())
				return
			}
			manager, err := h.Managers.ManagerOf(r.Context(), managed)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeError(w, http.StatusNotFound, "device is not managed")
					return
				}
				writeError(w, http.StatusInternalServerError, "lookup: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, managementPairResponse{ManagerLFDI: manager, ManagedLFDI: managed})
		default:
			writeError(w, http.StatusBadRequest, "manager or managed query param required")
		}
	}
}

// HandleRemoveManagementPair returns a handler for
// DELETE /api/management-pairs?managed=<LFDI>. 204 on success, 404 if the
// device has no manager.
func (h *AdminManagementHandler) HandleRemoveManagementPair() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		managed, err := normalizeLFDI(r.URL.Query().Get("managed"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "managed: "+err.Error())
			return
		}
		if err := h.Managers.Unassign(r.Context(), managed); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "device is not managed")
				return
			}
			writeError(w, http.StatusInternalServerError, "remove: "+err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// managementRekeyRequest is the body for POST /api/management-pairs/rekey.
type managementRekeyRequest struct {
	Role string `json:"role"` // "manager" or "managed"
	From string `json:"from"`
	To   string `json:"to"`
}

// HandleRekeyManagementPair returns a handler for
// POST /api/management-pairs/rekey. role selects whether "from"/"to" name a
// manager LFDI or a managed LFDI; every other pair naming the "from" LFDI is
// moved to "to" (role=manager), or the single pair naming it as the managed
// device is moved (role=managed). See store.RekeyManager/RekeyManaged for
// the refusal cases.
func (h *AdminManagementHandler) HandleRekeyManagementPair() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req managementRekeyRequest
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if req.Role != "manager" && req.Role != "managed" {
			writeError(w, http.StatusBadRequest, `role must be "manager" or "managed"`)
			return
		}
		from, err := normalizeLFDI(req.From)
		if err != nil {
			writeError(w, http.StatusBadRequest, "from: "+err.Error())
			return
		}
		to, err := normalizeLFDI(req.To)
		if err != nil {
			writeError(w, http.StatusBadRequest, "to: "+err.Error())
			return
		}

		var opErr error
		if req.Role == "manager" {
			opErr = h.Managers.RekeyManager(r.Context(), from, to)
		} else {
			opErr = h.Managers.RekeyManaged(r.Context(), from, to)
		}
		if opErr != nil {
			writeManagementRekeyError(w, opErr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"role": req.Role, "from": from, "to": to})
	}
}

// normalizeLFDI trims and upper-cases s, refusing anything that is not a
// valid 40-hex-digit HexBinary160. Every provisioning path normalizes case
// and refuses only a value that is not valid hexBinary or not 40 hex
// digits; a schema-valid lowercase LFDI is never refused.
func normalizeLFDI(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("required")
	}
	upper := strings.ToUpper(s)
	if !validLFDI(upper) {
		return "", errors.New("must be 40 hex characters")
	}
	return upper, nil
}

// writeManagementPairError maps store.Assign's sentinels to the create
// endpoint's response codes.
func writeManagementPairError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "device already has a different manager")
	case errors.Is(err, store.ErrInvalidManagementPair):
		// The store's own canonical-form refusal should never fire here:
		// normalizeLFDI already guarantees canonical input. Surfacing it
		// as 400 rather than 500 keeps that a visible, not a masked,
		// failure if the two ever disagree.
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "management pair: "+err.Error())
	}
}

// writeManagementRekeyError maps RekeyManager/RekeyManaged's sentinels to
// the rekey endpoint's response codes.
func writeManagementRekeyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "nothing to rekey")
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "target already has a manager")
	case errors.Is(err, store.ErrInvalidManagementPair):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "rekey: "+err.Error())
	}
}
