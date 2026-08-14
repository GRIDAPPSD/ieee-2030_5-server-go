// Package registration provides the IEEE 2030.5 Registration resource handler
// for GET /edev/{id}/rg. Ported from the reference server's
// internal/handler/registration.go; the auth.GetIdentity call is replaced by
// the injected IdentityFunc seam.
package registration

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// IdentityFunc extracts the authenticated device identity from the request
// context. Returns the LFDI, SFDI, and ok=true when identity is present.
// Replaces the direct auth.GetIdentity call in the original registration.go.
type IdentityFunc func(ctx context.Context) (lfdi, sfdi string, ok bool)

// HandleGetRegistration returns a handler for GET /edev/{id}/rg.
//
// Spec reference: IEEE 2030.5 section 10.6.4 (Registration resource shape)
// and CSIP V1.2 BASIC-004. The write path lives in the server's admin
// surface (GRIDAPPSD/ieee-2030_5-server-go#159) and persists Registration
// records into the same RegistrationStore this handler reads from.
//
// Authorization: the requesting client's TLS-derived LFDI (attached by the
// identity middleware) must equal the EndDevice's stored LFDI. Other devices
// receive 403 even if they can see the route.
//
// 200 + application/sep+xml when the caller's LFDI matches the EndDevice's
// LFDI and a Registration record exists.
// 403 if the caller has no TLS identity, or its LFDI does not match the
// EndDevice's LFDI.
// 404 if the EndDevice or its Registration does not exist.
// 405 on any non-GET / non-HEAD method.
func HandleGetRegistration(edevs store.EndDeviceStore, regs store.ResourceStore[sep2.Registration], identity IdentityFunc) http.HandlerFunc {
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
		lfdi, _, ok := identity(r.Context())
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
			srverr.Internal(w, r, fmt.Errorf("the owning-device lookup failed: %w", err))
			return
		}

		if dev.LFDI != lfdi {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		reg, err := regs.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		// Ensure the wire-format Href matches the canonical resource URL
		// even if the stored record was created without one (defensive:
		// the admin write always sets it, this is belt-and-suspenders).
		if reg.Href == "" {
			reg.Href = "/edev/" + id + "/rg"
		}

		encoding.WriteXML(w, http.StatusOK, &reg)
	}
}
