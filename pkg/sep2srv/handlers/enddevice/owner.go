package enddevice

import (
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/paging"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// OwnedBy reports whether a caller presenting callerLFDI owns the EndDevice
// whose stored LFDI is storedLFDI.
//
// It is true only when both are non-empty and exactly equal, with no case
// folding. Empty operands are refused here even when a caller has already
// checked them, so a record stored without an LFDI is owned by nobody.
func OwnedBy(storedLFDI, callerLFDI string) bool {
	return storedLFDI != "" && callerLFDI != "" && storedLFDI == callerLFDI
}

// HandleEndDeviceListForCaller returns a handler for GET /edev that lists only
// the EndDevice owned by the requesting certificate.
//
// The device is found with one GetByLFDI lookup and admitted through
// [OwnedBy]. All and Results count what this list holds, 0 or 1, never the
// store's total: a client pages by those counts. A caller with no record gets
// a well-formed empty list rather than a 404, because a device part way
// through registration still discovers itself through this collection.
//
// A request with no identity, or an empty LFDI, is refused with 403, the same
// answer the /edev/{id} ownership gate gives it. An empty list would instead
// assert "you have no EndDevice" about an identity the server never received.
//
// The filter is a predicate over the caller's identity. An aggregator model,
// where one certificate is authorized for several devices, generalizes it by
// resolving "the devices this certificate is authorized for" in place of the
// single lookup; the paging and counting below stay as they are.
func HandleEndDeviceListForCaller(s store.EndDeviceStore, identity IdentityFunc, pollRate uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		if identity == nil {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		callerLFDI, _, ok := identity(r.Context())
		if !ok || callerLFDI == "" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		if store.IsAbsent(s) {
			srverr.Internal(w, r, errors.New("enddevice: no EndDevice store is wired"))
			return
		}

		var result store.ListResult[sep2.EndDevice]
		dev, err := s.GetByLFDI(r.Context(), callerLFDI)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			srverr.Internal(w, r, fmt.Errorf("look up the caller's EndDevice by LFDI: %w", err))
			return
		case !OwnedBy(dev.LFDI, callerLFDI):
			log.Printf("enddevice: %s: the LFDI index returned a record with a different stored LFDI; listing nothing", srverr.Route(r))
		default:
			result = pageOfOne(dev, paging.ParseQuery(r.URL.Query()))
		}

		encoding.WriteXML(w, http.StatusOK, BuildEndDeviceList(r.URL.Path, result, pollRate))
	}
}

// pageOfOne applies the s and l paging parameters to a one-item collection.
// The a parameter orders by store key, which a lookup by LFDI does not carry,
// so it is not applied.
func pageOfOne(dev sep2.EndDevice, p paging.Params) store.ListResult[sep2.EndDevice] {
	result := store.ListResult[sep2.EndDevice]{All: 1}
	if p.Start == 0 && p.Limit > 0 {
		result.Results = 1
		result.Items = []sep2.EndDevice{dev}
	}
	return result
}
