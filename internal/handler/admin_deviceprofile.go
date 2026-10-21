package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Device profiles are server-only admin metadata (a name and a kind per LFDI):
//
//	GET /api/device-profiles          list every profile
//	PUT /api/device-profiles/{lfdi}   set the name and kind of one device
//
// POST /api/devices writes the same record through deviceProfileInputRefusal
// and DeviceProfileStore.Put, so the rules live in one place.

// AdminDeviceProfileHandler is the dependency surface of the profile routes.
type AdminDeviceProfileHandler struct {
	Profiles store.DeviceProfileStore

	// Managers supplies the pairs a kind change is checked against. Nil
	// leaves the check off.
	Managers store.EndDeviceManagementReader
}

// deviceProfileBody is the body of PUT /api/device-profiles/{lfdi}.
type deviceProfileBody struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// deviceProfileListResponse is the body of GET /api/device-profiles.
type deviceProfileListResponse struct {
	Profiles []store.DeviceProfile `json:"profiles"`
}

// deviceProfileInputRefusal returns the 400 message for a name or kind the
// store would not record, or "" when both are acceptable. An empty name means
// no name and an empty kind means not set. The message never echoes the name.
func deviceProfileInputRefusal(name, kind string) string {
	// A name is also refused when padded, which a program description is not.
	if name != "" && (!validAdminText(name) || name[0] == ' ' || name[len(name)-1] == ' ') {
		return "name: 1 to 32 printable ASCII characters with no leading or trailing space required"
	}
	switch kind {
	case "", store.DeviceKindAggregator, store.DeviceKindDevice:
		return ""
	}
	return `kind: must be "aggregator" or "device"`
}

// HandleListDeviceProfiles returns a handler for GET /api/device-profiles.
func (h *AdminDeviceProfileHandler) HandleListDeviceProfiles() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		profiles, err := h.Profiles.List(r.Context())
		if err != nil {
			writeDeviceProfileError(w, "list", err)
			return
		}
		writeJSON(w, http.StatusOK, deviceProfileListResponse{Profiles: profiles})
	}
}

// HandlePutDeviceProfile returns a handler for PUT /api/device-profiles/{lfdi}.
// A name another device holds, ignoring case, answers 409.
func (h *AdminDeviceProfileHandler) HandlePutDeviceProfile() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		lfdi, err := normalizeLFDI(r.PathValue("lfdi"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "lfdi: "+err.Error())
			return
		}
		var body deviceProfileBody
		dec := json.NewDecoder(io.LimitReader(r.Body, 1<<16))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil {
			srverr.LogBadRequest(r, err)
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		if msg := deviceProfileInputRefusal(body.Name, body.Kind); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		if msg, err := h.kindChangeRefusal(r.Context(), lfdi, body.Kind); err != nil {
			writeDeviceProfileError(w, "put", err)
			return
		} else if msg != "" {
			writeError(w, http.StatusConflict, msg)
			return
		}
		profile := store.DeviceProfile{LFDI: lfdi, Name: body.Name, Kind: body.Kind}
		if err := h.Profiles.Put(r.Context(), profile); err != nil {
			writeDeviceProfileError(w, "put", err)
			return
		}
		logDeviceProfileSet(profile)
		writeJSON(w, http.StatusOK, profile)
	}
}

// kindChangeRefusal returns the 409 message when setting kind on lfdi
// contradicts its pairs, or "". A managed device cannot become an aggregator
// and an aggregator that manages devices cannot become a device. Clearing the
// kind is always allowed: the derived kind then follows the pairs.
func (h *AdminDeviceProfileHandler) kindChangeRefusal(ctx context.Context, lfdi, kind string) (string, error) {
	if store.IsAbsent(h.Managers) {
		return "", nil
	}
	switch kind {
	case store.DeviceKindAggregator:
		manager, err := h.Managers.ManagerOf(ctx, lfdi)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return "device is managed by " + manager, nil
	case store.DeviceKindDevice:
		managed, err := h.Managers.ManagedBy(ctx, lfdi)
		if err != nil {
			return "", err
		}
		if len(managed) > 0 {
			return "device manages " + managed[0], nil
		}
	}
	return "", nil
}

// logDeviceProfileSet records a profile write by LFDI and escaped name.
func logDeviceProfileSet(p store.DeviceProfile) {
	log.Printf("admin device profile: set lfdi=%s name=%s", logsafe.Str(p.LFDI), logsafe.Str(p.Name))
}

// writeDeviceProfileError maps the store's sentinels to response codes. The
// 409 and 400 texts are fixed, so a name never reaches the client or the log
// through them; any other error is logged and answered without its text.
func writeDeviceProfileError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusConflict, "name already in use by another device")
	case errors.Is(err, store.ErrInvalidDeviceProfile):
		writeError(w, http.StatusBadRequest, "invalid device profile")
	default:
		log.Printf("admin device profile: %s: %v", logsafe.Str(op), logsafe.Err(err))
		writeError(w, http.StatusInternalServerError, op+": request failed, see server log")
	}
}
