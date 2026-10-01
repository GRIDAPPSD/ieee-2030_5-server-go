package flowreservation

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Notifier tells the subscribers of a resource href that it changed. Notify
// has no error return on purpose: a delivery failure is the notifier's to
// log, and never reaches the request that changed the list.
type Notifier interface {
	Notify(ctx context.Context, resourceHref string, status uint8)
}

// Option configures a Queue or a Canceller.
type Option func(*notifyHook)

// WithNotifier makes the Queue or Canceller notify subscribers of an
// EndDevice's FlowReservationResponseList whenever a response is created or
// a grant is cancelled. A nil n notifies no one.
func WithNotifier(n Notifier) Option {
	return func(h *notifyHook) { h.n = n }
}

type notifyHook struct{ n Notifier }

func newNotifyHook(opts []Option) notifyHook {
	var h notifyHook
	for _, o := range opts {
		o(&h)
	}
	return h
}

// fire enqueues one Changed notification for edevID's response list. The
// context is detached so a request that has already answered its client does
// not cancel the lookup of its subscribers.
func (h notifyHook) fire(ctx context.Context, edevID string) {
	if h.n == nil {
		return
	}
	h.n.Notify(context.WithoutCancel(ctx), ListHref(edevID), sep2.NotificationStatusChanged)
}

// ListHref is the href of edevID's FlowReservationResponseList, the resource
// a subscriber names.
func ListHref(edevID string) string {
	return "/edev/" + edevID + "/frp"
}

// NotifyingWriters returns w whose grant writer notifies after each grant it
// marks cancelled. That is the last write of both commitment.Ledger.CancelGrant
// and Ledger.Revise, so one wrapper covers a client cancel and a revision.
// Writers with no grant writer are returned unchanged, so the ledger still
// refuses them.
func NotifyingWriters(w commitment.Writers, n Notifier) commitment.Writers {
	if w.Grants == nil || n == nil {
		return w
	}
	w.Grants = notifyingGrants{inner: w.Grants, hook: notifyHook{n: n}}
	return w
}

type notifyingGrants struct {
	inner commitment.GrantWriter
	hook  notifyHook
}

func (g notifyingGrants) MarkCancelled(ctx context.Context, grant commitment.Grant, reason string, now int64) error {
	if err := g.inner.MarkCancelled(ctx, grant, reason, now); err != nil {
		return err
	}
	g.hook.fire(ctx, grant.EndDeviceID)
	return nil
}

// FRQLister is the part of the request store Pending reads.
type FRQLister interface {
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.FlowReservationRequest], error)
}

// FRPGetter is the part of the response store Pending reads.
type FRPGetter interface {
	Get(ctx context.Context, parentID, id string) (sep2.FlowReservationResponse, error)
}

// NewPendingCheck returns a function reporting whether any request under an
// EndDevice is still waiting for its response. A request withdrawn by its
// client is not waiting: the cancel answers it with a denial, and one whose
// cancel is half done is already disregarded (10.9.3.1). A store error is
// returned, never read as "nothing pending".
func NewPendingCheck(frq FRQLister, frp FRPGetter) func(ctx context.Context, edevID string) (bool, error) {
	return func(ctx context.Context, edevID string) (bool, error) {
		reqs, err := frq.List(ctx, edevID, store.ListOptions{Unbounded: true})
		if err != nil {
			return false, fmt.Errorf("flowreservation: list requests of %s: %w", edevID, err)
		}
		for _, r := range reqs.Items {
			if r.RequestStatus.RequestStatus == sep2.RequestStatusCancelled {
				continue
			}
			id, ok := requestID(edevID, r.Href)
			if !ok {
				return false, fmt.Errorf("flowreservation: request href %q is not under /edev/%s/frq/", r.Href, edevID)
			}
			_, err := frp.Get(ctx, edevID, id)
			switch {
			case err == nil:
			case errors.Is(err, store.ErrNotFound):
				return true, nil
			default:
				return false, fmt.Errorf("flowreservation: response of %s/%s: %w", edevID, id, err)
			}
		}
		return false, nil
	}
}
