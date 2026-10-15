package listhandler

import (
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/paging"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
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
	return SortedListHandler(s, store.SortByIDAsc, buildList, pollRate)
}

// SortedListHandler is [ListHandler] serving the list in the order sort names.
// A list with a normative ordering asks for [store.SortByListOrdering], and s
// must then provide it (see [store.Ordered]); a store that cannot answers
// [store.ErrUnsupportedSort], which is served as a 500 rather than as a list
// in some other order.
func SortedListHandler[T store.Copier[T], L any](
	s store.ResourceStore[T],
	sort store.SortKey,
	buildList func(href string, result store.ListResult[T], pollRate uint32) L,
	pollRate uint32,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}

		opts := paging.ParseQuery(r.URL.Query()).ToListOptions()
		opts.Sort = sort
		result, err := s.List(r.Context(), opts)
		if err != nil {
			srverr.Internal(w, r, err)
			return
		}

		list := buildList(r.URL.Path, result, pollRate)
		encoding.WriteXML(w, http.StatusOK, list)
	}
}
