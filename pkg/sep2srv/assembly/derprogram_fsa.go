package assembly

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	coreder "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/der"
	corelisthandler "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/listhandler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// errProgramViewReadOnly refuses a write through programsInFSA, whose reads
// hide programs of other FSAs and so cannot vouch for where a write lands.
var errProgramViewReadOnly = errors.New("assembly: the per-FSA DERProgram view is read-only")

// programsInFSA is the device-scoped DERProgram store narrowed to the
// programs whose own href names fsaID. The store keys programs by device
// alone, and the href that every writer builds with [coreder.DERProgramHref]
// is the only record of which FSA a program belongs to. A program whose href
// does not parse belongs to no FSA and is served under none; warn reports it.
//
// The list and single-program routes both read through this one type, so the
// two cannot disagree about which FSA a program is visible under.
type programsInFSA struct {
	store.ScopedStore[sep2.DERProgram]
	fsaID string
	warn  *unscopedProgramWarnings
}

func (v programsInFSA) holds(parentID string, p sep2.DERProgram) bool {
	_, fsa, _, ok := derhref.Program(p.Href)
	if !ok {
		v.warn.report(parentID, p.Href)
	}
	return ok && fsa == v.fsaID
}

func (v programsInFSA) Get(ctx context.Context, parentID, id string) (sep2.DERProgram, error) {
	p, err := v.ScopedStore.Get(ctx, parentID, id)
	if err != nil {
		return sep2.DERProgram{}, err
	}
	if !v.holds(parentID, p) {
		return sep2.DERProgram{}, store.ErrNotFound
	}
	return p, nil
}

// List serves only an unbounded read from the first item, which is the one
// read [store.OrderedScoped] makes before it sorts and pages; any other
// options are refused rather than paged a second time here.
func (v programsInFSA) List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERProgram], error) {
	if !opts.Unbounded || opts.Start != 0 || opts.After != "" {
		return store.ListResult[sep2.DERProgram]{}, fmt.Errorf("%w: the per-FSA DERProgram view serves only an unbounded read from the start", store.ErrInvalidListOptions)
	}
	whole, err := v.ScopedStore.List(ctx, parentID, opts)
	if err != nil {
		return store.ListResult[sep2.DERProgram]{}, err
	}
	if whole.All != uint32(len(whole.Items)) {
		return store.ListResult[sep2.DERProgram]{}, fmt.Errorf("store: unbounded read returned %d of %d items; cannot filter a partial collection", len(whole.Items), whole.All)
	}
	var items []sep2.DERProgram
	for _, p := range whole.Items {
		if v.holds(parentID, p) {
			items = append(items, p)
		}
	}
	n := uint32(len(items))
	return store.ListResult[sep2.DERProgram]{All: n, Results: n, Items: items}, nil
}

func (v programsInFSA) Count(ctx context.Context, parentID string) (uint32, error) {
	res, err := v.List(ctx, parentID, store.ListOptions{Unbounded: true})
	if err != nil {
		return 0, err
	}
	return res.All, nil
}

func (programsInFSA) Create(context.Context, string, string, sep2.DERProgram) error {
	return errProgramViewReadOnly
}

func (programsInFSA) Update(context.Context, string, string, sep2.DERProgram) error {
	return errProgramViewReadOnly
}

func (programsInFSA) Delete(context.Context, string, string) error {
	return errProgramViewReadOnly
}

// unscopedProgramWarnings logs each stored program whose href names no FSA,
// once per (EndDevice, href) for the life of the process: such a program is
// in no FSA's list and every request would otherwise repeat the line.
type unscopedProgramWarnings struct {
	seen sync.Map
	logf func(format string, args ...any)
}

func newUnscopedProgramWarnings(logf func(format string, args ...any)) *unscopedProgramWarnings {
	return &unscopedProgramWarnings{logf: logf}
}

func (w *unscopedProgramWarnings) report(parentID, href string) {
	if _, loaded := w.seen.LoadOrStore(parentID+"\x00"+href, struct{}{}); loaded {
		return
	}
	w.logf("assembly: DERProgram under EndDevice %q has href %q, which names no FSA; it is served under no FSA", parentID, href)
}

// derProgramListHandler serves GET /edev/{id}/fsa/{fsaId}/derp: the device's
// programs in FSA {fsaId}, in Table 50 order, paged after both.
func derProgramListHandler(programs store.ScopedStore[sep2.DERProgram], warn *unscopedProgramWarnings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		inFSA := programsInFSA{ScopedStore: programs, fsaID: r.PathValue("fsaId"), warn: warn}
		st := store.Under(store.OrderedScoped[sep2.DERProgram](inFSA, coreder.CompareDERProgram), r.PathValue("id"))
		corelisthandler.SortedListHandler(st, store.SortByListOrdering, coreder.BuildDERProgramList, 900).ServeHTTP(w, r)
	}
}

// derProgramHandler serves GET /edev/{id}/fsa/{fsaId}/derp/{derpId}, a 404
// for a program of another FSA, the same boundary the list route holds.
func derProgramHandler(programs store.ScopedStore[sep2.DERProgram], warn *unscopedProgramWarnings) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		inFSA := programsInFSA{ScopedStore: programs, fsaID: r.PathValue("fsaId"), warn: warn}
		scopedResourceHandler[sep2.DERProgram](inFSA, "id", "derpId", itemMethods{}, nil).ServeHTTP(w, r)
	}
}
