package devicelog

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/identity"
)

// KindNotification is the line kind for a Notification a device posted.
const KindNotification = "notification"

// ErrUnknownDevice is returned by NotificationRecorder.Record when the
// request carries no usable identity or the identity names no registered
// device. Nothing is recorded.
var ErrUnknownDevice = errors.New("devicelog: notification from an unknown device")

// NotificationRecorder writes a notification line to the posting device's log.
type NotificationRecorder struct {
	w          *Writer
	identity   identity.Func
	registered func(ctx context.Context, actor string) (bool, error)
}

// NewNotificationRecorder returns nil when w or identity is nil, because
// there is then nowhere to record a notification.
//
// registered reports whether an LFDI is a known device; a nil registered
// accepts any identity. It is the only registration decision: an accepted
// notification is filed to the device's own log, never through the writer's
// cached resolver, which could disagree.
func NewNotificationRecorder(w *Writer, identity identity.Func, registered func(ctx context.Context, actor string) (bool, error)) *NotificationRecorder {
	if w == nil || identity == nil {
		return nil
	}
	return &NotificationRecorder{w: w, identity: identity, registered: registered}
}

// Record queues one notification line keyed by the identity on ctx, never by
// a body value. A nil error means the line was queued, not written: a file
// error after that loses it and the writer counts the loss. An error means
// nothing was queued: ErrUnknownDevice for an identity that is missing or
// unregistered, otherwise the failed lookup or the writer's refusal (queue
// full, closed).
func (r *NotificationRecorder) Record(ctx context.Context, n sep2.Notification) error {
	actor, ok := Actor(ctx, r.identity)
	if !ok {
		return ErrUnknownDevice
	}
	if r.registered != nil {
		known, err := r.registered(ctx, actor)
		if err != nil {
			return fmt.Errorf("looking up the posting device: %w", err)
		}
		if !known {
			return ErrUnknownDevice
		}
	}
	fields := map[string]any{
		"status":             n.Status,
		"subscribedResource": logsafe.Str(n.SubscribedResource),
		"subscriptionURI":    logsafe.Str(n.SubscriptionURI),
	}
	if n.NewResourceURI != "" {
		fields["newResourceURI"] = logsafe.Str(n.NewResourceURI)
	}
	if err := r.w.AppendResolved(actor, "", nil, Entry{Kind: KindNotification, Fields: fields}); err != nil {
		return fmt.Errorf("queueing the notification line: %w", err)
	}
	return nil
}
