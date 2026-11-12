package flowreservation_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
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
