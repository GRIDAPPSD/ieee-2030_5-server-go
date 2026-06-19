package handler

import (
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/paging"
	"gitlab.pnnl.gov/arista/ieee-2030_5/ieee-2030_5-core/pkg/store"
)

// ListHandler creates a generic HTTP handler for any IEEE 2030.5 list resource.
// It handles paging (s, l, a query params) and XML serialization.
// The buildList function constructs the specific list type (EndDeviceList, etc.)
// from the store results.
//
// This pattern eliminates the list handler duplication seen in the existing server.
func ListHandler[T store.Copier[T], L any](
	s store.ResourceStore[T],
	buildList func(href string, result store.ListResult[T], pollRate uint32) L,
	pollRate uint32,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		params := paging.ParseQuery(r.URL.Query())
		result, err := s.List(r.Context(), params.ToListOptions())
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		list := buildList(r.URL.Path, result, pollRate)
		encoding.WriteXML(w, http.StatusOK, list)
	}
}
