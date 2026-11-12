package assembly

import (
	"log"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol/dercurve"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	corelisthandler "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/listhandler"
	coremetering "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// programExists answers store.ErrNotFound for a program that is not served
// under the request's device and FSA, the boundary the program routes hold.
func programExists(programs store.ScopedStore[sep2.DERProgram], warn *unscopedProgramWarnings) func(r *http.Request) error {
	return func(r *http.Request) error {
		inFSA := newProgramsInFSA(programs, r.PathValue("fsaId"), warn)
		_, err := inFSA.Get(r.Context(), r.PathValue("id"), r.PathValue("derpId"))
		return err
	}
}

// derExists answers store.ErrNotFound for a DER not held under the request's device.
func derExists(ders store.ScopedStore[sep2.DER]) func(r *http.Request) error {
	return func(r *http.Request) error {
		_, err := ders.Get(r.Context(), r.PathValue("id"), r.PathValue("derId"))
		return err
	}
}

// programCurveListBuilder lists the curve catalog under one program with each
// member's href moved to the program-scoped route. No store records which
// curves belong to which program: a control may name any stored curve, so every
// program lists the whole catalog, and a member resolves under the program that
// lists it.
func programCurveListBuilder(href string, result store.ListResult[sep2.DERCurve], pollRate uint32) sep2.DERCurveList {
	list := coreder.BuildDERCurveList(href, result, pollRate)
	for i := range list.DERCurve {
		id, ok := dercurve.ID(list.DERCurve[i].Href)
		if !ok {
			log.Printf("assembly: DERCurve with href %s names no curve id; listing it as stored", logsafe.Quote(list.DERCurve[i].Href))
			continue
		}
		list.DERCurve[i].Href = href + "/" + id
	}
	return list
}

// programCurveListHandler serves GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dc.
func programCurveListHandler(curves store.ResourceStore[sep2.DERCurve], programs store.ScopedStore[sep2.DERProgram], warn *unscopedProgramWarnings) http.HandlerFunc {
	return requireParent(programExists(programs, warn), corelisthandler.SortedListHandler[sep2.DERCurve, sep2.DERCurveList](
		store.Ordered(curves, coreder.CompareDERCurve), store.SortByListOrdering, programCurveListBuilder, 900,
	))
}

// programCurveHandler serves GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dc/{dcId}:
// the stored curve under its program-scoped href, the one the list gives it.
func programCurveHandler(curves store.ResourceStore[sep2.DERCurve], programs store.ScopedStore[sep2.DERProgram], warn *unscopedProgramWarnings) http.HandlerFunc {
	return requireParent(programExists(programs, warn), func(w http.ResponseWriter, r *http.Request) {
		curve, err := curves.Get(r.Context(), r.PathValue("dcId"))
		if err != nil {
			writeLookupFailure(w, r, err)
			return
		}
		curve.Href = r.URL.Path
		encoding.WriteXML(w, http.StatusOK, &curve)
	})
}

// associatedProgramListHandler serves GET /edev/{id}/der/{derId}/derp: every
// program stored under the device, whatever its FSA, in Table 50 order. A
// program is stored per device and not per DER, so the DERs of one device
// share one list.
func associatedProgramListHandler(ders store.ScopedStore[sep2.DER], programs store.ScopedStore[sep2.DERProgram], warn *unscopedProgramWarnings) http.HandlerFunc {
	return requireParent(derExists(ders), func(w http.ResponseWriter, r *http.Request) {
		allFSAs := newProgramsInAnyFSA(programs, warn)
		st := store.Under(store.OrderedScoped[sep2.DERProgram](allFSAs, coreder.CompareDERProgram), r.PathValue("id"))
		corelisthandler.SortedListHandler(st, store.SortByListOrdering, coreder.BuildDERProgramList, 900).ServeHTTP(w, r)
	})
}

// storedDER reads the DER's stored record, the association record the two
// routes below resolve. ok is false once a response has been written.
func storedDER(w http.ResponseWriter, r *http.Request, ders store.ScopedStore[sep2.DER]) (sep2.DER, bool) {
	d, err := ders.Get(r.Context(), r.PathValue("id"), r.PathValue("derId"))
	if err != nil {
		writeLookupFailure(w, r, err)
		return d, false
	}
	return d, true
}

// currentProgramHandler serves GET /edev/{id}/der/{derId}/cdp: the program the
// stored DER's CurrentDERProgramLink names. A DER with no such link has no
// current program (404), and a link naming a program of another device is
// refused the same way, so a stored record cannot reach across devices.
func currentProgramHandler(ders store.ScopedStore[sep2.DER], programs store.ScopedStore[sep2.DERProgram], warn *unscopedProgramWarnings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, ok := storedDER(w, r, ders)
		if !ok {
			return
		}
		if d.CurrentDERProgramLink == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		edev, fsa, derp, ok := derhref.Program(d.CurrentDERProgramLink.Href)
		if !ok || edev != r.PathValue("id") {
			log.Printf("assembly: CurrentDERProgramLink %s of a DER under %s names no program of that device; answering 404",
				logsafe.Quote(d.CurrentDERProgramLink.Href), logsafe.Str(r.PathValue("id")))
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		prog, err := newProgramsInFSA(programs, fsa, warn).Get(r.Context(), edev, derp)
		if err != nil {
			writeLookupFailure(w, r, err)
			return
		}
		encoding.WriteXML(w, http.StatusOK, &prog)
	}
}

// associatedUsagePointHandler serves GET /edev/{id}/der/{derId}/upt: the
// UsagePoint the stored DER's AssociatedUsagePointLink names (/upt/{id}).
func associatedUsagePointHandler(ders store.ScopedStore[sep2.DER], usagePoints store.ResourceStore[sep2.UsagePoint]) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, ok := storedDER(w, r, ders)
		if !ok {
			return
		}
		if d.AssociatedUsagePointLink == nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		id, ok := coremetering.UsagePointIDFromHref(d.AssociatedUsagePointLink.Href)
		if !ok {
			log.Printf("assembly: AssociatedUsagePointLink %s of a DER under %s names no UsagePoint; answering 404",
				logsafe.Quote(d.AssociatedUsagePointLink.Href), logsafe.Str(r.PathValue("id")))
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		upt, err := usagePoints.Get(r.Context(), id)
		if err != nil {
			writeLookupFailure(w, r, err)
			return
		}
		encoding.WriteXML(w, http.StatusOK, &upt)
	}
}
