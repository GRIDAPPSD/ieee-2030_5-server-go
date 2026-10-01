package flowreservation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ErrIncompleteRecoverDeps is returned when RecoverDeps lacks a store or the
// queue.
var ErrIncompleteRecoverDeps = errors.New("flowreservation: RecoverDeps needs FRQ, FRP, Lifecycles and Queue")

// RecoverFRQ is the part of the request store Recover walks.
type RecoverFRQ interface {
	Parents(ctx context.Context) ([]string, error)
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.FlowReservationRequest], error)
}

// RecoverFRP is the part of the response store Recover reads, and the delete
// that takes back a revision which no longer fits.
type RecoverFRP interface {
	FRPReader
	Delete(ctx context.Context, parentID, id string) error
}

// RecoverDeps is what Recover reads and writes. Ledger and Writers are only
// used to finish an interrupted cancel or revision; Writers should be the
// ones the Canceller uses, so a finished grant notifies as a live one does.
type RecoverDeps struct {
	FRQ        RecoverFRQ
	FRP        RecoverFRP
	Lifecycles dercontrol.LifecycleReader
	Queue      *Queue
	Ledger     *commitment.Ledger
	Writers    commitment.Writers
	// Notifier, when set, receives each change made to a grant after the
	// fleet lock is released, as Canceller does. Pass the one the writers
	// notify through.
	Notifier Notifier
}

// RecoverCounts is what one Recover pass did. A request counts under every
// case it needed, so Rearmed through RevisionsRolledBack can sum to more than
// Scanned minus Untouched.
type RecoverCounts struct {
	Scanned                int
	Rearmed                int // pending, deadline timer restored
	DeniedCancelled        int // cancelled with no response, denied
	GrantsCancelled        int // cancelled request whose live grant was cancelled
	RevisionsRolledForward int // two live responses, older ones cancelled
	RevisionsRolledBack    int // two live responses, tip no longer fit and was deleted
	Untouched              int
	Failed                 int // requests whose repair failed, logged and skipped
}

// readError marks a failure to read a store, which fails the whole pass.
type readError struct{ err error }

func (e *readError) Error() string { return e.err.Error() }
func (e *readError) Unwrap() error { return e.err }

// Recover is the startup pass over every stored request. It restores what a
// restart or a crash between two store writes left behind, so each request
// ends with exactly one live response (10.9.3.2):
//
//   - a pending request gets its deadline timer back, measured from its own
//     creationTime (Queue.Rearm);
//   - a cancelled request with no response is denied;
//   - a cancelled request whose chain tip is still a live grant has that grant
//     and its executions cancelled;
//   - a chain with two live responses, a revision stopped half way, is
//     completed (Ledger.CompleteRevision).
//
// It takes the locks the live paths take (the queue's key lock inside
// Answer, then the fleet lock inside the ledger), never notifies under one,
// and may run against a queue that is already serving. A failure to read a
// store, including listing requests or walking a chain, ends the pass with
// an error; a failure to repair one request is logged, counted in Failed and
// skipped. The counts are returned either way.
func Recover(ctx context.Context, deps RecoverDeps, now time.Time) (RecoverCounts, error) {
	var counts RecoverCounts
	if deps.FRQ == nil || deps.FRP == nil || deps.Lifecycles == nil || deps.Queue == nil {
		return counts, ErrIncompleteRecoverDeps
	}
	parents, err := deps.FRQ.Parents(ctx)
	if err != nil {
		return counts, fmt.Errorf("flowreservation: recover: list request owners: %w", err)
	}
	for _, edevID := range parents {
		page, err := deps.FRQ.List(ctx, edevID, store.ListOptions{Unbounded: true})
		if err != nil {
			return counts, fmt.Errorf("flowreservation: recover: list requests of %s: %w", edevID, err)
		}
		for _, frq := range page.Items {
			if err := ctx.Err(); err != nil {
				return counts, err
			}
			counts.Scanned++
			err := recoverRequest(ctx, deps, edevID, frq, now, &counts)
			var re *readError
			switch {
			case errors.As(err, &re):
				return counts, fmt.Errorf("flowreservation: recover: %w", err)
			case err != nil:
				counts.Failed++
				log.Printf("flowreservation: recover: %s %s: skipped: %v", edevID, frq.Href, err)
			}
		}
	}
	log.Printf("flowreservation: recover: scanned %d requests: rearmed %d, denied %d, grants cancelled %d, revisions rolled forward %d and back %d, untouched %d, failed %d",
		counts.Scanned, counts.Rearmed, counts.DeniedCancelled, counts.GrantsCancelled,
		counts.RevisionsRolledForward, counts.RevisionsRolledBack, counts.Untouched, counts.Failed)
	return counts, nil
}

// chainState is a request's responses with each one's cancel mark read.
type chainState struct {
	chain     []sep2.FlowReservationResponse
	cancelled []bool
}

func (s chainState) tip() sep2.FlowReservationResponse { return s.chain[len(s.chain)-1] }

// liveIndexes returns the positions of the responses with no cancel mark.
func (s chainState) liveIndexes() []int {
	var live []int
	for i, c := range s.cancelled {
		if !c {
			live = append(live, i)
		}
	}
	return live
}

func readChain(ctx context.Context, deps RecoverDeps, edevID, frqID string) (chainState, error) {
	chain, err := ChainOf(ctx, deps.FRP, edevID, frqID)
	if err != nil {
		return chainState{}, &readError{err}
	}
	st := chainState{chain: chain, cancelled: make([]bool, len(chain))}
	for i, frp := range chain {
		id, ok := ResponseID(edevID, frp.Href)
		if !ok {
			return chainState{}, fmt.Errorf("response href %q is not under /edev/%s/frp/", frp.Href, edevID)
		}
		lc, err := deps.Lifecycles.Get(ctx, edevID, id)
		switch {
		case err == nil:
			st.cancelled[i] = lc.CancelledAt != nil
		case errors.Is(err, store.ErrNotFound):
		default:
			return chainState{}, &readError{fmt.Errorf("lifecycle of response %s/%s: %w", edevID, id, err)}
		}
	}
	return st, nil
}

func recoverRequest(ctx context.Context, deps RecoverDeps, edevID string, frq sep2.FlowReservationRequest, now time.Time, counts *RecoverCounts) error {
	frqID, ok := requestID(edevID, frq.Href)
	if !ok {
		return fmt.Errorf("request href %q is not under /edev/%s/frq/", frq.Href, edevID)
	}
	st, err := readChain(ctx, deps, edevID, frqID)
	if err != nil {
		return err
	}
	cancelled := frq.RequestStatus.RequestStatus == sep2.RequestStatusCancelled

	if len(st.chain) == 0 {
		if cancelled {
			// Answer notifies through the queue's own hook, outside its key lock.
			if _, err := deps.Queue.Answer(ctx, edevID, frqID, Decision{Kind: Deny}); err != nil && !errors.Is(err, ErrAlreadyAnswered) {
				return fmt.Errorf("deny cancelled request: %w", err)
			}
			counts.DeniedCancelled++
			return nil
		}
		deps.Queue.Rearm(edevID, frqID, frq, now)
		counts.Rearmed++
		return nil
	}

	if ctxN, flush := deferNotifications(ctx, deps.Notifier); flush != nil {
		ctx = ctxN
		defer flush()
	}

	acted := false
	if live := st.liveIndexes(); len(live) >= 2 {
		forward, err := completeRevision(ctx, deps, edevID, st, live, now)
		if err != nil {
			return err
		}
		if forward {
			counts.RevisionsRolledForward++
		} else {
			counts.RevisionsRolledBack++
		}
		acted = true
		if st, err = readChain(ctx, deps, edevID, frqID); err != nil {
			return err
		}
	}

	if cancelled {
		tip := st.tip()
		tipLive := !st.cancelled[len(st.chain)-1] && tip.Interval != nil && tip.Interval.Duration > 0
		if tipLive {
			if deps.Ledger == nil {
				return commitment.ErrNoLedger
			}
			err := deps.Ledger.CancelGrant(ctx, deps.Writers, tip.MRID, cancelReason, now.Unix())
			var conflict *commitment.ConflictError
			if err != nil && !(errors.As(err, &conflict) && conflict.Code == commitment.ConflictGrantNotLive) {
				return fmt.Errorf("finish cancel of grant %s: %w", tip.MRID, err)
			}
			counts.GrantsCancelled++
			acted = true
		}
	}
	if !acted {
		counts.Untouched++
	}
	return nil
}

// completeRevision finishes the revision whose older live responses sit at
// the positions in live and whose tip is the last of the chain.
func completeRevision(ctx context.Context, deps RecoverDeps, edevID string, st chainState, live []int, now time.Time) (rolledForward bool, err error) {
	tipIdx := len(st.chain) - 1
	tip := st.chain[tipIdx]
	var olds []string
	for _, i := range live {
		if i != tipIdx {
			olds = append(olds, st.chain[i].MRID)
		}
	}
	if deps.Ledger == nil {
		return false, commitment.ErrNoLedger
	}
	tipID, ok := ResponseID(edevID, tip.Href)
	if !ok || len(olds) == 0 {
		return false, fmt.Errorf("complete revision: no older live response before %s", tip.MRID)
	}
	deleteTip := func(ctx context.Context) error { return deps.FRP.Delete(ctx, edevID, tipID) }
	forward, err := deps.Ledger.CompleteRevision(ctx, deps.Writers, olds, tip.MRID, now.Unix(), deleteTip)
	if err != nil {
		return false, fmt.Errorf("complete revision %v -> %s: %w", olds, tip.MRID, err)
	}
	outcome := "rolled back, the revision no longer fit"
	if forward {
		outcome = "rolled forward"
	}
	log.Printf("flowreservation: recover: revision of %s/%s: %v -> %s: %s", edevID, tipID, olds, tip.MRID, outcome)
	return forward, nil
}

// deferNotifications is DeferNotifications that does nothing without a
// notifier.
func deferNotifications(ctx context.Context, n Notifier) (context.Context, func()) {
	if n == nil {
		return ctx, nil
	}
	return DeferNotifications(ctx, n)
}
