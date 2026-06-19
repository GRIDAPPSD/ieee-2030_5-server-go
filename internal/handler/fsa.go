package handler

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/sep2"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store/memory"
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
func HandleFSA(fsaStore *memory.ScopedStore[sep2.FunctionSetAssignments]) http.HandlerFunc {
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
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		encoding.WriteXML(w, http.StatusOK, &fsa)
	}
}

// HandleCreateFSA returns a handler for POST /api/fsa (admin endpoint).
// Creates a FunctionSetAssignment and assigns it to a device.
func HandleCreateFSA(fsaStore *memory.ScopedStore[sep2.FunctionSetAssignments]) http.HandlerFunc {
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
			http.Error(w, "create FSA failed", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Location", fsa.Href)
		encoding.WriteXML(w, http.StatusCreated, &fsa)
	}
}
