// Package fsa provides IEEE 2030.5 FunctionSetAssignments resource handlers.
// Ported verbatim from the reference server's internal/handler/fsa.go
// (no auth touch points; import paths rewritten to core).
package fsa

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// BuildFSAList constructs a FunctionSetAssignmentsList from store results.
func BuildFSAList(href string, result store.ListResult[sep2.FunctionSetAssignments], pollRate uint32) sep2.FunctionSetAssignmentsList {
	return sep2.FunctionSetAssignmentsList{
		ListResource: sep2.ListResource{
			SubscribableResource: sep2.SubscribableResource{
				Resource: sep2.Resource{Href: href},
			},
			All:      result.All,
			Results:  result.Results,
			PollRate: pollRate,
		},
		FunctionSetAssignments: result.Items,
	}
}

// HandleFSA returns a handler for GET /edev/{id}/fsa/{fsaId}.
func HandleFSA(fsaStore store.ScopedStore[sep2.FunctionSetAssignments]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		edevID := r.PathValue("id")
		fsaID := r.PathValue("fsaId")

		fsa, err := fsaStore.Get(r.Context(), edevID, fsaID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				// Return empty default FSA with DERProgramListLink
				defaultFSA := sep2.FunctionSetAssignments{
					Resource:           sep2.Resource{Href: fmt.Sprintf("/edev/%s/fsa/%s", edevID, fsaID)},
					DERProgramListLink: &sep2.ListLink{Href: fmt.Sprintf("/edev/%s/fsa/%s/derp", edevID, fsaID)},
				}
				encoding.WriteXML(w, http.StatusOK, &defaultFSA)
				return
			}
			srverr.Internal(w, r, err)
			return
		}

		encoding.WriteXML(w, http.StatusOK, &fsa)
	}
}

// HandleCreateFSA returns a handler for POST /api/fsa (admin endpoint).
// Creates a FunctionSetAssignment and assigns it to a device.
//
// This handler serves the admin-surface create route and is intended to be
// wired by the consuming server on its own admin mux, NOT by
// assembly.BuildProtocolRouter, consistent with this package's read/protocol
// export scope. The export is deliberate: consumers mount it on their own
// admin auth chain.
func HandleCreateFSA(fsaStore store.ScopedStore[sep2.FunctionSetAssignments]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Admin API uses JSON, but FSA creation can be simple
		edevID := r.URL.Query().Get("edevId")
		if edevID == "" {
			http.Error(w, "edevId query param required", http.StatusBadRequest)
			return
		}

		fsaID := fmt.Sprintf("fsa-%d", time.Now().UnixNano()%100000)
		fsa := sep2.FunctionSetAssignments{
			Resource:           sep2.Resource{Href: fmt.Sprintf("/edev/%s/fsa/%s", edevID, fsaID)},
			MRID:               fsaID,
			Description:        "Function Set Assignment",
			DERProgramListLink: &sep2.ListLink{Href: fmt.Sprintf("/edev/%s/fsa/%s/derp", edevID, fsaID)},
		}

		if err := fsaStore.Create(r.Context(), edevID, fsaID, fsa); err != nil {
			srverr.InternalMessage(w, r, "create FSA failed", err)
			return
		}

		w.Header().Set("Location", fsa.Href)
		encoding.WriteXML(w, http.StatusCreated, &fsa)
	}
}
