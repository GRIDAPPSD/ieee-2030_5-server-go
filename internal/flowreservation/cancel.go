package flowreservation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// maxCancelPasses bounds how often Cancel re-walks a chain that a concurrent
// Revise keeps extending.
const maxCancelPasses = 4

// ErrChainMoving is returned when a request's revision chain kept growing
// through every cancel pass; the request is Cancelled, so repeating the call
// finishes the work.
var ErrChainMoving = errors.New("flowreservation: response chain kept changing during the cancel")

// cancelReason is the reason recorded on a grant and its executions when the
// client withdraws the request that earned them.
const cancelReason = "client cancel"

// FRQStore is the subset of the request store a cancel needs: it reads the
// request and writes its RequestStatus back.
type FRQStore interface {
	Get(ctx context.Context, parentID, id string) (sep2.FlowReservationRequest, error)
	Update(ctx context.Context, parentID, id string, resource sep2.FlowReservationRequest) error
}

// Canceller withdraws a FlowReservationRequest at the client's instruction
// (10.9.3.1). The zero value is not usable: construct with NewCanceller.
type Canceller struct {
	frq     FRQStore
	frp     FRPStore
	queue   *Queue
	ledger  *commitment.Ledger
	writers commitment.Writers
	notify  notifyHook
}

// NewCanceller builds a Canceller. queue answers a pending request; ledger
// and writers cancel an answered one through commitment.Ledger.CancelGrant,
// the only path that cancels a grant and its executions. A nil ledger or
// writers refuse an answered cancel (fail closed) and never a pending one.
// WithNotifier notifies the response list's subscribers once a grant is
// cancelled; a pending request's denial notifies through the queue.
func NewCanceller(frq FRQStore, frp FRPStore, queue *Queue, ledger *commitment.Ledger, writers commitment.Writers, opts ...Option) *Canceller {
	h := newNotifyHook(opts)
	if h.n != nil {
		writers = NotifyingWriters(writers, h.n)
	}
	return &Canceller{frq: frq, frp: frp, queue: queue, ledger: ledger, writers: writers, notify: h}
}

// Cancel marks the request Cancelled with status's dateTime, then settles its
// answer. A request with no response yet gets one zero-duration response in
// that same step and leaves the queue (10.9.3.2: a response is created for
// each request; 10.9.3.1: a cancelled request is disregarded). One already
// answered with a grant has the grant and every DER control executing it
// cancelled. Calling it again on a cancelled request finishes whatever an
// earlier failed call left undone and changes nothing else.
//
// The pending answer goes through Queue.Answer, so a cancel racing the
// deadline hold lands on the same exactly-once check: whichever creates the
// response first wins, and the loser finds it answered.
func (c *Canceller) Cancel(ctx context.Context, edevID, frqID string, status sep2.RequestStatus) error {
	frq, err := c.frq.Get(ctx, edevID, frqID)
	if err != nil {
		return fmt.Errorf("flowreservation: get FlowReservationRequest %s/%s: %w", edevID, frqID, err)
	}
	if frq.RequestStatus.RequestStatus != sep2.RequestStatusCancelled {
		frq.RequestStatus = status
		if err := c.frq.Update(ctx, edevID, frqID, frq); err != nil {
			return fmt.Errorf("flowreservation: mark %s/%s cancelled: %w", edevID, frqID, err)
		}
	}

	_, err = c.queue.Answer(ctx, edevID, frqID, Decision{Kind: Deny})
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrAlreadyAnswered) {
		return err
	}

	// A revision (#668) stores a new response per change, so the request's
	// answer is a chain, not one response.
	// Every pass's grant writes notify only after the passes end, once the
	// fleet lock is released, and one device is notified once.
	if c.notify.n != nil {
		var flush func()
		ctx, flush = DeferNotifications(ctx, c.notify.n)
		defer flush()
	}
	var unresolved []string
	for range maxCancelPasses {
		chain, err := ChainOf(ctx, c.frp, edevID, frqID)
		if err != nil {
			return err
		}
		if len(chain) == 0 {
			return fmt.Errorf("flowreservation: get FlowReservationResponse %s/%s: %w", edevID, frqID, store.ErrNotFound)
		}
		gone, err := c.cancelChain(ctx, chain)
		if err != nil {
			return err
		}
		// A Revise that stored its response after the walk cancelled the
		// member we held, so that member's cancel read as "not live" and
		// the new tip is still live: walk again until the chain holds still.
		// A member the ledger did not know means the walk saw a state that
		// has since changed (another response may now hold its id), so that
		// pass settles nothing even when the length is unchanged.
		after, err := ChainOf(ctx, c.frp, edevID, frqID)
		if err != nil {
			return err
		}
		if len(gone) == 0 && len(after) == len(chain) {
			return nil
		}
		unresolved = gone
	}
	if len(unresolved) > 0 {
		return fmt.Errorf("%w: not known to the ledger: %s", ErrChainMoving, strings.Join(unresolved, ", "))
	}
	return ErrChainMoving
}

// cancelChain cancels every live grant in chain. More than one can be live
// after a Revise whose rollback failed, so the tip alone is not enough. A
// member that is already cancelled is skipped.
func (c *Canceller) cancelChain(ctx context.Context, chain []sep2.FlowReservationResponse) (gone []string, err error) {
	for _, frp := range chain {
		// A denial or a response with no interval commits nothing: there is
		// no grant to cancel and no execution can name it.
		if frp.Interval == nil || frp.Interval.Duration == 0 {
			continue
		}
		if c.ledger == nil {
			return gone, commitment.ErrNoLedger
		}
		err = c.ledger.CancelGrant(ctx, c.writers, frp.MRID, cancelReason, sep2time.Now().Unix())
		var conflict *commitment.ConflictError
		if errors.As(err, &conflict) && conflict.Code == commitment.ConflictGrantNotLive {
			continue
		}
		// A member seen mid-revision and rolled back cleanly is gone by now,
		// and the pass is repeated to see what replaced it.
		if errors.Is(err, commitment.ErrNoGrant) {
			gone = append(gone, frp.MRID)
			continue
		}
		if err != nil {
			return gone, err
		}
	}
	return gone, nil
}
