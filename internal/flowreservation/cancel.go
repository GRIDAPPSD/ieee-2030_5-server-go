package flowreservation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// maxCancelPasses bounds how often Cancel re-walks a chain that a concurrent
// Revise keeps extending.
const maxCancelPasses = 4

// ErrChainMoving is returned when a request's revision chain kept changing
// through every cancel pass. When the chain only kept growing, repeating the
// call finishes the work. When a member stayed unknown to the ledger the
// error is an *UnresolvedGrantsError, and repeating the call does not help.
var ErrChainMoving = errors.New("flowreservation: response chain kept changing during the cancel")

// UnresolvedGrantsError is ErrChainMoving for chain members the commitment
// ledger could not resolve on any pass. The ledger reaches a grant only
// through its EndDevice's fleet, so this is a device that belongs to no
// fleet: its LFDI is gone, or the device record is. Its grants stay live
// until the device has an LFDI again and the cancel is repeated, or the
// EndDevice is deleted, which removes its requests and responses with it.
type UnresolvedGrantsError struct {
	// MRIDs are the members' mRIDs, in chain order.
	MRIDs []string
}

func (e *UnresolvedGrantsError) Error() string {
	return fmt.Sprintf("%v: not known to the ledger: %s", ErrChainMoving, strings.Join(e.MRIDs, ", "))
}

// Is reports a match for ErrChainMoving.
func (e *UnresolvedGrantsError) Is(target error) bool { return target == ErrChainMoving }

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

	client := Attribution{Kind: KindClient, At: sep2time.Now().Unix()}
	_, err = c.queue.Answer(ctx, edevID, frqID, Decision{Kind: Deny, By: client})
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrAlreadyAnswered) {
		return err
	}

	cancelled, err := c.CancelGrants(ctx, edevID, frqID, cancelReason)
	c.recordCancels(ctx, edevID, cancelled, client)
	return err
}

// CancelGrants cancels every live grant of the request's answer, and every
// DER control executing one, without touching the request itself: the
// operator's cancel. More than one member can be live after a Revise whose
// rollback failed, so the whole chain is cancelled, not only its tip. It
// returns the store ids of the responses it cancelled, so the caller can
// record who cancelled them; a member already cancelled is not among them.
// They are returned with any error too, since a cancel that fails part way
// keeps what it cancelled. A request with no response returns
// store.ErrNotFound.
func (c *Canceller) CancelGrants(ctx context.Context, edevID, frqID, reason string) (cancelled []CancelledGrant, err error) {
	// A revision (#668) stores a new response per change, so the request's
	// answer is a chain, not one response.
	//
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
			return cancelled, err
		}
		if len(chain) == 0 {
			return cancelled, fmt.Errorf("flowreservation: get FlowReservationResponse %s/%s: %w", edevID, frqID, store.ErrNotFound)
		}
		done, gone, err := c.cancelChain(ctx, chainMembers(frqID, chain), reason)
		cancelled = append(cancelled, done...)
		if err != nil {
			return cancelled, err
		}
		// A Revise that stored its response after the walk cancelled the
		// member we held, so that member's cancel read as "not live" and
		// the new tip is still live: walk again until the chain holds still.
		// A member the ledger did not know means the walk saw a state that
		// has since changed (another response may now hold its id), so that
		// pass settles nothing even when the length is unchanged.
		after, err := ChainOf(ctx, c.frp, edevID, frqID)
		if err != nil {
			return cancelled, err
		}
		if len(gone) == 0 && len(after) == len(chain) {
			return cancelled, nil
		}
		unresolved = gone
	}
	if len(unresolved) > 0 {
		return cancelled, &UnresolvedGrantsError{MRIDs: unresolved}
	}
	return cancelled, ErrChainMoving
}

func (c *Canceller) answers() *Answers {
	if c.queue == nil {
		return nil
	}
	return c.queue.Answers()
}

// CancelledGrant is one response a cancel marked cancelled: its store id and
// its mRID.
type CancelledGrant struct {
	ID, MRID string
}

// recordCancels records by as the canceller of each response cancelled. The
// cancels already happened, so a failed record is logged and the response
// reads as unrecorded.
func (c *Canceller) recordCancels(ctx context.Context, edevID string, cancelled []CancelledGrant, by Attribution) {
	for _, g := range cancelled {
		if err := c.answers().RecordCancel(context.WithoutCancel(ctx), edevID, g.ID, by); err != nil {
			log.Printf("ERROR: flowreservation: response %s/%s was cancelled, but by whom is unrecorded: %v", edevID, g.ID, err)
		}
	}
}

// chainMember is a response with the store id the chain walk read it under.
type chainMember struct {
	id  string
	frp sep2.FlowReservationResponse
}

// chainMembers pairs each response of ChainOf's result with its store id,
// which the walk's position fixes, so no id is parsed back out of an href.
func chainMembers(frqID string, chain []sep2.FlowReservationResponse) []chainMember {
	members := make([]chainMember, len(chain))
	id := frqID
	for i, frp := range chain {
		if i > 0 {
			id = RevisionID(id)
		}
		members[i] = chainMember{id: id, frp: frp}
	}
	return members
}

// cancelChain cancels every live grant in members and returns those it
// cancelled. A member that is already cancelled is skipped.
func (c *Canceller) cancelChain(ctx context.Context, members []chainMember, reason string) (cancelled []CancelledGrant, gone []string, err error) {
	for _, m := range members {
		frp := m.frp
		// A denial or a response with no interval commits nothing: there is
		// no grant to cancel and no execution can name it.
		if frp.Interval == nil || frp.Interval.Duration == 0 {
			continue
		}
		if c.ledger == nil {
			return cancelled, gone, commitment.ErrNoLedger
		}
		err = c.ledger.CancelGrant(ctx, c.writers, frp.MRID, reason, sep2time.Now().Unix())
		var conflict *commitment.ConflictError
		if errors.As(err, &conflict) && conflict.Code == commitment.ConflictGrantNotLive {
			continue
		}
		// A member seen mid-revision and rolled back cleanly is gone by now,
		// and the pass is repeated to see what replaced it. A member whose
		// EndDevice has no LFDI has no fleet to lock, which no pass changes.
		if errors.Is(err, commitment.ErrNoGrant) || errors.Is(err, commitment.ErrNoLFDI) {
			gone = append(gone, frp.MRID)
			continue
		}
		if err != nil {
			return cancelled, gone, err
		}
		cancelled = append(cancelled, CancelledGrant{ID: m.id, MRID: frp.MRID})
	}
	return cancelled, gone, nil
}
