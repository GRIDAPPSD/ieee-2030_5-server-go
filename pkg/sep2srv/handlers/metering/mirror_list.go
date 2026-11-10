package metering

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/paging"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// HandleMirrorUsagePointList returns the handler for GET /mup. It lists the
// mirrors the caller may act for, decided by the same mirrorActor every other
// /mup route uses: the mirrored device is the caller, or the caller is its
// current manager. A mirror the caller may not act for is neither listed nor
// counted in "all". Paging applies to what the caller sees.
func HandleMirrorUsagePointList(
	s store.ResourceStore[sep2.MirrorUsagePoint],
	managers store.EndDeviceManagementReader,
	lfdiProvider LFDIProvider,
	pollRate uint32,
	opts ...MirrorOption,
) http.HandlerFunc {
	actFor := newMirrorActor(managers, opts...)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			encoding.MethodNotAllowed(w, "GET, HEAD")
			return
		}
		lfdi, ok := lfdiProvider.require(w, r)
		if !ok {
			return
		}

		visible, err := visibleMirrors(r.Context(), s, actFor, lfdi)
		if err != nil {
			srverr.Internal(w, r, err)
			return
		}
		page := paging.ParseQuery(r.URL.Query()).ToListOptions()
		list := BuildMirrorUsagePointList(r.URL.Path, pageOf(visible, page), pollRate)
		encoding.WriteXML(w, http.StatusOK, list)
	}
}

// visibleMirrors returns, in ascending id order, the mirrors caller may act
// for.
func visibleMirrors(ctx context.Context, s store.ResourceStore[sep2.MirrorUsagePoint], actFor mirrorActor, caller string) ([]sep2.MirrorUsagePoint, error) {
	all, err := s.List(ctx, store.ListOptions{Unbounded: true, Sort: store.SortByIDAsc})
	if err != nil {
		return nil, fmt.Errorf("list the MirrorUsagePoints: %w", err)
	}
	var visible []sep2.MirrorUsagePoint
	for _, mup := range all.Items {
		decision, err := actFor(ctx, caller, mup.DeviceLFDI)
		if err != nil {
			return nil, fmt.Errorf("the ownership check failed: %w", err)
		}
		if decision == mirrorAllowed {
			visible = append(visible, mup)
		}
	}
	return visible, nil
}

// pageOf applies the a, s and l paging parameters to an ascending list whose
// ids are the tails of the items' hrefs, so "all" is the size of the list the
// caller can see.
func pageOf(visible []sep2.MirrorUsagePoint, opts store.ListOptions) store.ListResult[sep2.MirrorUsagePoint] {
	items := paging.Slice(visible, func(m sep2.MirrorUsagePoint) string { return strings.TrimPrefix(m.Href, "/mup/") }, opts)
	return store.ListResult[sep2.MirrorUsagePoint]{All: uint32(len(visible)), Results: uint32(len(items)), Items: items}
}
