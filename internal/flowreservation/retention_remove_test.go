package flowreservation_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// Remove has no grace: a chain that ended seconds ago goes, with every record
// keyed like it, where the sweep would still hold it, and subscribers of the
// response list hear of it once.
func TestRetentionRemove_EndedChainGoesWithoutGraceAndNotifies(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	end := now.Unix() - 10
	ids := f.seedChain(t, "frq-1", end-600, member{start: end - 600, dur: 600, created: end - 600})
	rec := &hrefRecorder{}
	r := f.retention()
	r.Notifier = rec

	if n, err := r.Sweep(context.Background(), now); err != nil || n != 0 {
		t.Fatalf("Sweep inside the grace = %d, %v; want 0, nil", n, err)
	}
	if err := r.Remove(context.Background(), aggID, "frq-1", now); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	noneHeld(t, f.held(t, "frq-1", ids))
	if want := []string{flowreservation.ListHref(aggID)}; len(rec.hrefs) != 1 || rec.hrefs[0] != want[0] {
		t.Errorf("notified %v, want %v", rec.hrefs, want)
	}
}

// A live or unanswered request is kept whole and nobody is told.
func TestRetentionRemove_ActiveOrUnansweredRequestIsRefusedAndKept(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	live := f.seedChain(t, "live", now.Unix()-60, member{start: now.Unix() - 60, dur: 3600, created: now.Unix() - 60})
	f.pendingRequest(t, "pending", now.Unix()-60, windowRequest("REQ-pending", now.Unix(), 600, 1000))
	rec := &hrefRecorder{}
	r := f.retention()
	r.Notifier = rec

	for _, id := range []string{"live", "pending"} {
		if err := r.Remove(context.Background(), aggID, id, now); !errors.Is(err, flowreservation.ErrRequestActive) {
			t.Errorf("Remove(%s) = %v, want ErrRequestActive", id, err)
		}
	}
	for k, v := range f.held(t, "live", live) {
		if !v && !strings.HasPrefix(k, "lc ") { // seedChain stores a lifecycle record only for a cancelled member
			t.Errorf("%s removed by a refused Remove", k)
		}
	}
	if _, err := f.frq.Get(context.Background(), aggID, "pending"); err != nil {
		t.Errorf("pending request after a refused Remove: %v", err)
	}
	if len(rec.hrefs) != 0 {
		t.Errorf("notified %v after refusals, want nothing", rec.hrefs)
	}
}

func TestRetentionRemove_MissingRequestIsNotFoundAndIncompleteDepsRefuse(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	if err := f.retention().Remove(context.Background(), aggID, "nope", now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("Remove of a missing request = %v, want ErrNotFound", err)
	}
	r := f.retention()
	r.Ledger = nil
	if err := r.Remove(context.Background(), aggID, "nope", now); !errors.Is(err, flowreservation.ErrIncompleteRetention) {
		t.Errorf("Remove without a ledger = %v, want ErrIncompleteRetention", err)
	}
}

// faultyFRP wraps the response store with faults armed by the test: Get
// fails from its getFailFrom-th call on (0 never), and Delete fails for the
// ids in failDelete.
type faultyFRP struct {
	*memory.ScopedStore[sep2.FlowReservationResponse]
	getFailFrom int
	gets        int
	failDelete  map[string]bool
}

var errFault = errors.New("injected store fault")

func (s *faultyFRP) Get(ctx context.Context, parentID, id string) (sep2.FlowReservationResponse, error) {
	s.gets++
	if s.getFailFrom > 0 && s.gets >= s.getFailFrom {
		return sep2.FlowReservationResponse{}, errFault
	}
	return s.ScopedStore.Get(ctx, parentID, id)
}

func (s *faultyFRP) Delete(ctx context.Context, parentID, id string) error {
	if s.failDelete[id] {
		return errFault
	}
	return s.ScopedStore.Delete(ctx, parentID, id)
}

// A read that fails before any delete step (the chain read again under the
// fleet lock) is an error, never "active", changes nothing and tells nobody.
func TestRetentionRemove_ReadFailureBeforeAnyDeleteIsAnErrorAndSilent(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	end := now.Unix() - 10
	ids := f.seedChain(t, "frq-1", end-600, member{start: end - 600, dur: 600, created: end - 600})
	rec := &hrefRecorder{}
	r := f.retention()
	r.Notifier = rec
	// Reads 1 and 2 are the unlocked chain walk (the response, then the
	// missing revision); read 3 is the first of the walk under the lock.
	r.FRP = &faultyFRP{ScopedStore: f.frp, getFailFrom: 3}

	err := r.Remove(context.Background(), aggID, "frq-1", now)
	if !errors.Is(err, errFault) || errors.Is(err, flowreservation.ErrRequestActive) {
		t.Fatalf("Remove = %v, want the injected fault and not ErrRequestActive", err)
	}
	for k, v := range f.held(t, "frq-1", ids) {
		if !v && k != "lc frq-1" { // seedChain stores a lifecycle record only for a cancelled member
			t.Errorf("%s removed by a Remove that failed before deleting", k)
		}
	}
	if len(rec.hrefs) != 0 {
		t.Errorf("notified %v after a failure that removed nothing, want nobody", rec.hrefs)
	}
}

// A delete that fails part way through the chain answers an error, not
// "active", and subscribers hear of what was already removed.
func TestRetentionRemove_FailurePartWayThroughTheChainIsAnErrorAndNotifies(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	end := now.Unix() - 10
	ids := f.seedChain(t, "frq-1", end-1200,
		member{start: end - 1200, dur: 600, created: end - 1200},
		member{start: end - 600, dur: 600, created: end - 600},
	)
	rec := &hrefRecorder{}
	r := f.retention()
	r.Notifier = rec
	// The tip goes first and the request's own response last, so failing the
	// last delete leaves the tip and the request already gone.
	r.FRP = &faultyFRP{ScopedStore: f.frp, failDelete: map[string]bool{"frq-1": true}}

	err := r.Remove(context.Background(), aggID, "frq-1", now)
	if !errors.Is(err, errFault) || errors.Is(err, flowreservation.ErrRequestActive) {
		t.Fatalf("Remove = %v, want the injected fault and not ErrRequestActive", err)
	}
	got := f.held(t, "frq-1", ids)
	if got["frp "+ids[1]] || got["frq frq-1"] || !got["frp frq-1"] {
		t.Errorf("records after the fault = %v, want the tip and the request gone and the first response kept", got)
	}
	if len(rec.hrefs) != 1 || rec.hrefs[0] != flowreservation.ListHref(aggID) {
		t.Errorf("notified %v, want one notification of the response list", rec.hrefs)
	}
}

// The same Remove, whose first delete is the one that fails, removed nothing
// and tells nobody.
func TestRetentionRemove_FailureOnTheFirstDeleteNotifiesNobody(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	now := time.Now()
	end := now.Unix() - 10
	ids := f.seedChain(t, "frq-1", end-1200,
		member{start: end - 1200, dur: 600, created: end - 1200},
		member{start: end - 600, dur: 600, created: end - 600},
	)
	rec := &hrefRecorder{}
	r := f.retention()
	r.Notifier = rec
	r.FRP = &faultyFRP{ScopedStore: f.frp, failDelete: map[string]bool{ids[1]: true}}

	if err := r.Remove(context.Background(), aggID, "frq-1", now); !errors.Is(err, errFault) {
		t.Fatalf("Remove = %v, want the injected fault", err)
	}
	if len(rec.hrefs) != 0 {
		t.Errorf("notified %v after a Remove that deleted nothing, want nobody", rec.hrefs)
	}
}

// ErrIncompleteRetention names Grace as Sweep's requirement only, since a
// Remove with no Grace set is valid and returns it for another missing part.
func TestRetentionRemove_IncompleteErrorDoesNotDemandGraceOfRemove(t *testing.T) {
	t.Parallel()
	f := newRetentionFixture(t)
	r := f.retention()
	r.Grace = 0
	r.Ledger = nil
	err := r.Remove(context.Background(), aggID, "nope", time.Now())
	if !errors.Is(err, flowreservation.ErrIncompleteRetention) {
		t.Fatalf("Remove = %v, want ErrIncompleteRetention", err)
	}
	if !strings.Contains(err.Error(), "(and a positive Grace for Sweep)") {
		t.Errorf("error %q does not scope the Grace requirement to Sweep", err)
	}
	if _, err := r.Sweep(context.Background(), time.Now()); !errors.Is(err, flowreservation.ErrIncompleteRetention) {
		t.Errorf("Sweep without Grace = %v, want ErrIncompleteRetention", err)
	}
}
