package metering

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// UsagePointTree holds the stores of the server-authored /upt hierarchy, so a
// delete of one record can remove everything stored beneath it.
type UsagePointTree struct {
	UsagePoints   store.ResourceStore[sep2.UsagePoint]
	MeterReadings store.ScopedStore[sep2.MeterReading]
	ReadingSets   store.ScopedStore[sep2.ReadingSet]
	Readings      store.ScopedStore[sep2.Reading]
	ReadingTypes  store.ScopedStore[sep2.ReadingType]
}

// cascadeStep is one removal of children from one store. probe refuses the
// step before anything is removed; run does the removal and counts it.
type cascadeStep struct {
	name  string
	store string
	probe func(ctx context.Context) error
	run   func(ctx context.Context) (uint32, error)
}

func parentStep[T store.Copier[T]](name string, s store.ScopedStore[T], parent string) cascadeStep {
	return cascadeStep{
		name:  name,
		store: fmt.Sprintf("%T", s),
		probe: func(ctx context.Context) error { return store.ProbeParent(ctx, s, parent) },
		run:   func(ctx context.Context) (uint32, error) { return store.CascadeParent(ctx, s, parent) },
	}
}

func prefixStep[T store.Copier[T]](name string, s store.ScopedStore[T], prefix string) cascadeStep {
	return cascadeStep{
		name:  name,
		store: fmt.Sprintf("%T", s),
		probe: func(ctx context.Context) error { return store.ProbePrefix(ctx, s, prefix) },
		run:   func(ctx context.Context) (uint32, error) { return store.CascadePrefix(ctx, s, prefix) },
	}
}

// cascade probes every step, then runs them, so a store that cannot cascade
// or cannot be read refuses the delete before any child is removed. The
// caller deletes its own record afterwards, so a failure leaves the record in
// place and the request can be retried. A failing step is named with its
// store, and the removed count is logged against target.
func cascade(ctx context.Context, target string, steps ...cascadeStep) error {
	for _, s := range steps {
		if err := s.probe(ctx); err != nil {
			return fmt.Errorf("cascade delete step %q (store %s) refused, so the record is left in place: %w", s.name, s.store, err)
		}
	}
	var removed uint32
	for _, s := range steps {
		n, err := s.run(ctx)
		removed += n
		if err != nil {
			return fmt.Errorf("cascade delete step %q (store %s) failed after %d record(s) removed, so the record is left in place: %w",
				s.name, s.store, removed, err)
		}
	}
	log.Printf("upt: DELETE %s removed %d record(s) beneath it", logsafe.Quote(target), removed)
	return nil
}

// finishDelete answers a delete after its cascade: a miss is a 404 and any
// other failure a 500.
func finishDelete(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case err != nil:
		srverr.Internal(w, r, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleDeleteUsagePoint returns the handler for DELETE /upt/{uptId}. It
// removes the UsagePoint and everything stored under it: a record left behind
// would be served again under a re-created UsagePoint of the same id.
//
// It takes no authority decision: it is mounted behind the manager-only check.
func HandleDeleteUsagePoint(t UsagePointTree) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		upt, ok := ScopeKey(r.PathValue("uptId"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if _, err := t.UsagePoints.Get(ctx, upt); err != nil {
			finishDelete(w, r, err)
			return
		}
		// A client disconnect must not stop the removal part-way.
		ctx = context.WithoutCancel(ctx)
		err := cascade(ctx, r.URL.Path,
			parentStep("meter readings", t.MeterReadings, upt),
			parentStep("reading types", t.ReadingTypes, upt),
			prefixStep("reading sets", t.ReadingSets, upt+"/"),
			prefixStep("readings", t.Readings, upt+"/"),
		)
		if err == nil {
			err = t.UsagePoints.Delete(ctx, upt)
		}
		finishDelete(w, r, err)
	}
}

// HandleDeleteMeterReading returns the handler for DELETE
// /upt/{uptId}/mr/{mrId}, removing the MeterReading, its ReadingType, its
// ReadingSets and every Reading beneath it. Like HandleDeleteUsagePoint it is
// mounted behind the manager-only check.
func HandleDeleteMeterReading(t UsagePointTree) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		key, ok := ReadingSetScopeKey(r.PathValue("uptId"), r.PathValue("mrId"))
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		upt, mr := r.PathValue("uptId"), r.PathValue("mrId")
		if _, err := t.MeterReadings.Get(ctx, upt, mr); err != nil {
			finishDelete(w, r, err)
			return
		}
		ctx = context.WithoutCancel(ctx)
		err := cascade(ctx, r.URL.Path,
			parentStep("readings", t.Readings, key),
			prefixStep("readings", t.Readings, key+"/"),
			parentStep("reading sets", t.ReadingSets, key),
		)
		if err == nil {
			// The ReadingType is optional beside a MeterReading, so none is fine.
			if rtErr := t.ReadingTypes.Delete(ctx, upt, mr); rtErr != nil && !errors.Is(rtErr, store.ErrNotFound) {
				err = rtErr
			}
		}
		if err == nil {
			err = t.MeterReadings.Delete(ctx, upt, mr)
		}
		finishDelete(w, r, err)
	}
}

// HandleDeleteReadingSet returns the handler for DELETE
// /upt/{uptId}/mr/{mrId}/rs/{rsId}, removing the ReadingSet and its Readings.
// Like HandleDeleteUsagePoint it is mounted behind the manager-only check.
func HandleDeleteReadingSet(t UsagePointTree) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		setKey, ok := ReadingSetScopeKey(r.PathValue("uptId"), r.PathValue("mrId"))
		readingKey, readingOK := ReadingScopeKey(r.PathValue("uptId"), r.PathValue("mrId"), r.PathValue("rsId"))
		if !ok || !readingOK {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		rs := r.PathValue("rsId")
		if _, err := t.ReadingSets.Get(ctx, setKey, rs); err != nil {
			finishDelete(w, r, err)
			return
		}
		ctx = context.WithoutCancel(ctx)
		err := cascade(ctx, r.URL.Path,
			parentStep("readings", t.Readings, readingKey))
		if err == nil {
			err = t.ReadingSets.Delete(ctx, setKey, rs)
		}
		finishDelete(w, r, err)
	}
}
