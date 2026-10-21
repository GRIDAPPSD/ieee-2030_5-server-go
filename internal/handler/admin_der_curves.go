package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol/dercurve"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// CurveLister lists the global DERCurve store.
type CurveLister interface {
	List(ctx context.Context, opts store.ListOptions) (store.ListResult[sep2.DERCurve], error)
}

// CurveInfo is one stored curve as GET /api/der/curves lists it: ID is what
// a control names it by.
type CurveInfo struct {
	ID          string `json:"id"`
	CurveType   uint8  `json:"curveType"`
	Description string `json:"description"`
}

// HandleListCurves returns the handler for GET /api/der/curves: every stored
// curve, in id order, as {"curves": [...]}, with [] and not null when none
// is stored. A curve whose href names no id cannot be linked by a control,
// so it fails the whole request rather than being left out.
func HandleListCurves(curves CurveLister) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		res, err := curves.List(r.Context(), store.ListOptions{Unbounded: true})
		if err != nil {
			slog.Error("admin: DER curve list failed", "event", "der_curve_list_failed", "error", logsafe.Err(err))
			writeError(w, http.StatusInternalServerError, "curve read failed, see server log")
			return
		}
		out := make([]CurveInfo, 0, len(res.Items))
		for _, c := range res.Items {
			id, ok := dercurve.ID(c.Href)
			if !ok {
				slog.Error("admin: stored DER curve has no id in its href", "event", "der_curve_list_failed", "href", logsafe.Str(c.Href))
				writeError(w, http.StatusInternalServerError, "curve read failed, see server log")
				return
			}
			out = append(out, CurveInfo{ID: id, CurveType: c.CurveType, Description: c.Description})
		}
		writeJSON(w, http.StatusOK, map[string]any{"curves": out})
	}
}
