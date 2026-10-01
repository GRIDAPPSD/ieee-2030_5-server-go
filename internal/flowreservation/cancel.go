package flowreservation

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

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

	// A revision (#668) replaces the response stored under frqID, so the
	// live one is the chain's tip.
	chain, err := ChainOf(ctx, c.frp, edevID, frqID)
	if err != nil {
		return err
	}
	if len(chain) == 0 {
		return fmt.Errorf("flowreservation: get FlowReservationResponse %s/%s: %w", edevID, frqID, store.ErrNotFound)
	}
	frp := chain[len(chain)-1]
	// A denial or a response with no interval commits nothing: there is no
	// grant to cancel and no execution can name it.
	if frp.Interval == nil || frp.Interval.Duration == 0 {
		return nil
	}
	if c.ledger == nil {
		return commitment.ErrNoLedger
	}
	// The grant writer notifies only after CancelGrant returns, once the
	// fleet lock is released.
	if c.notify.n != nil {
		var flush func()
		ctx, flush = DeferNotifications(ctx, c.notify.n)
		defer flush()
	}
	err = c.ledger.CancelGrant(ctx, c.writers, frp.MRID, cancelReason, sep2time.Now().Unix())
	var conflict *commitment.ConflictError
	if errors.As(err, &conflict) && conflict.Code == commitment.ConflictGrantNotLive {
		return nil
	}
	return err
}
