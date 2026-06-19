package handler

import (
	"errors"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2/encoding"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
)

// IEEE-101: SEP2-protocol GET handler for the Registration resource at
// /edev/{id}/rg.
//
// Spec reference: IEEE 2030.5 §10.6.4 (Registration resource shape) and CSIP
// V1.2 BASIC-004 (a device GETs its EndDevice.RegistrationLink, reads the
// PIN, and validates it against its configured PIN before accepting any
// other control). The write path lives in admin_register.go (IEEE-095) and
// persists Registration records into the same RegistrationStore this handler
// reads from.
//
// Authorization: the requesting client's TLS-derived LFDI (attached by
// auth.IdentityMiddleware) must equal the EndDevice's stored LFDI. Other
// devices receive 403 even if they can see the route — devices may not
// learn each other's PINs. Admin reads happen on the separate /api/*
// surface (IEEE-094 admin-listener split) and are out of scope for this
// SEP2-protocol handler.

// HandleGetRegistration returns a handler for GET /edev/{id}/rg.
//
// 200 + application/sep+xml when the caller's LFDI matches the EndDevice's
// LFDI and a Registration record exists.
// 403 if the caller has no TLS identity, or its LFDI does not match the
// EndDevice's LFDI.
// 404 if the EndDevice or its Registration does not exist.
// 405 on any non-GET / non-HEAD method.
func HandleGetRegistration(edevs store.EndDeviceStore, regs *memory.RegistrationStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		id := r.PathValue("id")
		if id == "" {
			http.Error(w, "device id required", http.StatusBadRequest)
			return
		}

		// Ownership gate: requesting cert must match the device. Run this
		// before the store lookups so we don't leak presence-by-timing.
		identity, ok := auth.GetIdentity(r.Context())
		if !ok {
			http.Error(w, "identity required", http.StatusForbidden)
			return
		}

		dev, err := edevs.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		if dev.LFDI != identity.LFDI {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		reg, err := regs.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Ensure the wire-format Href matches the canonical resource URL
		// even if the stored record was created without one (defensive —
		// IEEE-095's admin write always sets it).
		if reg.Href == "" {
			reg.Href = "/edev/" + id + "/rg"
		}

		encoding.WriteXML(w, http.StatusOK, &reg)
	}
}
