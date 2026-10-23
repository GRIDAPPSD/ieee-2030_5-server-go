package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
	sep2lfdi "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/lfdi"
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

	// Profiles supplies each LFDI's stored kind for the create and rekey
	// kind gate. Nil leaves only the stored-kind rules off; the rules that
	// read pairs alone still run.
	Profiles store.DeviceProfileReader

	// EndDevices and Activity feed the per-LFDI health a pair listing
	// carries: whether the LFDI has an EndDevice record and when it last
	// made a request. A pair is keyed on the LFDI, which changes when a
	// certificate rotates, so a pair whose LFDI has no record or has gone
	// quiet is the operator's rotation signal. Nil EndDevices omits
	// hasRecord; nil Activity leaves lastSeen null.
	EndDevices store.EndDeviceReader
	Activity   *activity.Recorder
}

// pairMemberHealth is one side of a pair as the listing reports it.
// HasRecord is nil when the handler holds no EndDevice reader, so "unknown"
// is not read as "no record". LastSeen is an RFC 3339 UTC time, or null when
// the LFDI has made no request since the server started.
type pairMemberHealth struct {
	LFDI      string  `json:"lfdi"`
	HasRecord *bool   `json:"hasRecord,omitempty"`
	LastSeen  *string `json:"lastSeen"`
}

// memberHealth reads one LFDI's record presence and last request. A lookup
// failure other than ErrNotFound is returned, never reported as "no record".
func (h *AdminManagementHandler) memberHealth(ctx context.Context, lfdi string) (pairMemberHealth, error) {
	m := pairMemberHealth{LFDI: lfdi}
	if h.EndDevices != nil {
		_, err := h.EndDevices.GetByLFDI(ctx, lfdi)
		switch {
		case err == nil:
			has := true
			m.HasRecord = &has
		case errors.Is(err, store.ErrNotFound):
			has := false
			m.HasRecord = &has
		default:
			return pairMemberHealth{}, err
		}
	}
	m.LastSeen = h.Activity.LastRFC3339(lfdi)
	return m, nil
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

		if !h.kindAllowedManager(r.Context(), w, "create", managerLFDI) ||
			!h.kindAllowedManaged(r.Context(), w, "create", managedLFDI) {
			return
		}
		if err := h.Managers.Assign(r.Context(), managerLFDI, managedLFDI); err != nil {
			writeManagementPairError(w, "create", err)
			return
		}
		writeJSON(w, http.StatusCreated, managementPairResponse{ManagerLFDI: managerLFDI, ManagedLFDI: managedLFDI})
	}
}

// HandleListManagementPairs returns a handler for GET /api/management-pairs.
// Exactly one of "manager" or "managed" selects the direction: by manager
// lists every device it manages (possibly empty), by managed device returns
// its single manager (404 if unmanaged). Each LFDI in the answer also gets a
// health entry (record presence, last request), the operator's signal that a
// certificate rotated and a pair needs the rekey endpoint.
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
				writeManagementInternalError(w, "list", err)
				return
			}
			mgr, err := h.memberHealth(r.Context(), manager)
			if err != nil {
				writeManagementInternalError(w, "list", err)
				return
			}
			members := make([]pairMemberHealth, 0, len(managed))
			for _, m := range managed {
				health, err := h.memberHealth(r.Context(), m)
				if err != nil {
					writeManagementInternalError(w, "list", err)
					return
				}
				members = append(members, health)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"managerLFDI":  manager,
				"managedLFDIs": managed,
				"manager":      mgr,
				"managed":      members,
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
				writeManagementInternalError(w, "lookup", err)
				return
			}
			mgr, err := h.memberHealth(r.Context(), manager)
			if err != nil {
				writeManagementInternalError(w, "lookup", err)
				return
			}
			dev, err := h.memberHealth(r.Context(), managed)
			if err != nil {
				writeManagementInternalError(w, "lookup", err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"managerLFDI": manager,
				"managedLFDI": managed,
				"manager":     mgr,
				"managed":     dev,
			})
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
			writeManagementInternalError(w, "remove", err)
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

		// Rekeying a manager makes "to" the manager of everything "from"
		// managed; rekeying a managed device makes "to" the managed side
		// of its existing manager.
		if req.Role == "manager" {
			if !h.kindAllowedManager(r.Context(), w, "rekey", to) {
				return
			}
		} else if !h.kindAllowedManaged(r.Context(), w, "rekey", to) {
			return
		}

		var opErr error
		if req.Role == "manager" {
			opErr = h.Managers.RekeyManager(r.Context(), from, to)
		} else {
			opErr = h.Managers.RekeyManaged(r.Context(), from, to)
		}
		if opErr != nil {
			writeManagementRekeyError(w, req.Role, opErr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"role": req.Role, "from": from, "to": to})
	}
}

// kindAllowedManager and kindAllowedManaged write a 409 and return false when
// a pair would break a kind rule, or a 500 when a lookup fails. Create checks
// both sides, rekey the side that changes.
func (h *AdminManagementHandler) kindAllowedManager(ctx context.Context, w http.ResponseWriter, op, manager string) bool {
	msg, err := h.kindGate().managerRefusal(ctx, manager)
	return h.writeKindRefusal(w, op, msg, err)
}

func (h *AdminManagementHandler) kindAllowedManaged(ctx context.Context, w http.ResponseWriter, op, managed string) bool {
	msg, err := h.kindGate().managedRefusal(ctx, managed)
	return h.writeKindRefusal(w, op, msg, err)
}

func (h *AdminManagementHandler) kindGate() kindGate {
	return kindGate{profiles: h.Profiles, managers: h.Managers}
}

func (h *AdminManagementHandler) writeKindRefusal(w http.ResponseWriter, op, msg string, err error) bool {
	if err != nil {
		writeManagementInternalError(w, op, err)
		return false
	}
	if msg != "" {
		writeError(w, http.StatusConflict, msg)
		return false
	}
	return true
}

// normalizeLFDI trims and upper-cases s, refusing anything that is not a
// valid 40-hex-digit HexBinary160. Every provisioning path normalizes case
// and refuses only a value that is not valid hexBinary or not 40 hex
// digits; a schema-valid lowercase LFDI is never refused.
func normalizeLFDI(s string) (string, error) {
	if strings.TrimSpace(s) == "" {
		return "", errors.New("required")
	}
	upper, ok := sep2lfdi.CanonicalTrimSpace(s)
	if !ok {
		return "", errors.New("must be 40 hex characters")
	}
	return upper, nil
}

// writeManagementPairError maps store.Assign's sentinels to the create
// endpoint's response codes. op names the operation for the internal-error
// log line (writeManagementInternalError).
func writeManagementPairError(w http.ResponseWriter, op string, err error) {
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
		writeManagementInternalError(w, op, err)
	}
}

// writeManagementRekeyError maps RekeyManager/RekeyManaged's sentinels to
// the rekey endpoint's response codes. role distinguishes a manager-side
// collision (the target already manages other devices) from a managed-side
// one (the target already has a manager): both map to the same
// store.ErrAlreadyExists, and only role tells them apart.
func writeManagementRekeyError(w http.ResponseWriter, role string, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "nothing to rekey")
	case errors.Is(err, store.ErrAlreadyExists):
		if role == "manager" {
			writeError(w, http.StatusConflict, "target already manages other devices")
		} else {
			writeError(w, http.StatusConflict, "target already has a manager")
		}
	case errors.Is(err, store.ErrInvalidManagementPair):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeManagementInternalError(w, "rekey", err)
	}
}

// writeManagementInternalError answers a 500 for a store failure that does
// not map to one of the sentinels above: chiefly a failed durable write
// (store.EndDeviceManagementStore#677 fix round, H9). That error wraps
// atomicfile's own error, which names the absolute snapshot path on disk;
// the admin plane is authenticated, but a filesystem path is still
// server-internal detail a response must not echo back to the caller
// (secure-coding.md section 6). The full error, path included, still goes
// to the server log, where an operator debugging a write failure needs it.
func writeManagementInternalError(w http.ResponseWriter, op string, err error) {
	log.Printf("admin management pairs: %s: %v", op, err)
	writeError(w, http.StatusInternalServerError, op+": request failed, see server log")
}
