package adminplane

import (
	"context"
	"log"
	"reflect"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

// notifyRemover mirrors the unexported interface core uses to detect and
// extract NotifyRemoved from the notifier. Defined here at the consumer
// per the interface-at-consumer discipline. core's *subscription.Manager
// satisfies it.
type notifyRemover interface {
	NotifyRemoved(ctx context.Context, sub sep2.Subscription) error
}

// Compile-time guard: *coresub.Manager is the production notifier type
// that MUST implement notifyRemover. If a future refactor of core drops
// NotifyRemoved from *coresub.Manager, this line fails to compile
// instead of silently regressing to the swallowed-notification path that
// caused the original CORE-019 failure. The blank-var pattern avoids
// allocating at runtime; the compiler discards it entirely.
var _ notifyRemover = (*coresub.Manager)(nil)

// notifierAdapter wraps handler.ResourceNotifier so its value satisfies
// assembly.ResourceNotifier (which is coreedev.ResourceNotifier). Both
// interfaces have the identical Notify method set:
//
//	Notify(ctx context.Context, resourceHref string, status uint8)
//
// The adapter also forwards NotifyRemoved when the inner notifier supports
// it. Core's subscription DELETE handler calls NotifyRemoved (via type
// assertion) to dispatch the final Removed Notification (CSIP V1.2 Section 11.6);
// without this forwarding the notification is silently dropped.
//
// The adapter is safe when notifier is nil (the adapter is nil in that
// case, not a non-nil interface wrapping a nil concrete value).
type notifierAdapter struct{ inner handler.ResourceNotifier }

func (a *notifierAdapter) Notify(ctx context.Context, resourceHref string, status uint8) {
	// inner is guaranteed non-nil by AdaptNotifier; a nil handler.ResourceNotifier
	// produces a nil *notifierAdapter, not a non-nil adapter wrapping nil.
	a.inner.Notify(ctx, resourceHref, status)
}

// NotifyRemoved forwards to the inner notifier when it satisfies the
// notifyRemover interface (i.e. the inner is *coresub.Manager or any
// other concrete type that implements the method). Returns nil when the
// inner does not implement NotifyRemoved; this is safe for core which
// treats a nil return as "no final notification."
func (a *notifierAdapter) NotifyRemoved(ctx context.Context, sub sep2.Subscription) error {
	if nr, ok := a.inner.(notifyRemover); ok {
		return nr.NotifyRemoved(ctx, sub)
	}
	return nil
}

type notificationURIValidator interface {
	ValidateNotificationURI(ctx context.Context, uri string) error
}

var _ notificationURIValidator = (*coresub.Manager)(nil)

// ValidateNotificationURI forwards to the inner notifier's destination
// policy. An inner notifier without one gets the default policy, so wrapping
// never relaxes creation-time validation.
func (a *notifierAdapter) ValidateNotificationURI(ctx context.Context, uri string) error {
	if v, ok := a.inner.(notificationURIValidator); ok {
		return v.ValidateNotificationURI(ctx, uri)
	}
	return coresub.DestinationPolicy{}.ValidateNotificationURI(ctx, uri)
}

type subscriberCheckSetter interface {
	SetSubscriberCheck(fn coresub.SubscriberCheck)
}

var _ subscriberCheckSetter = (*coresub.Manager)(nil)

// SetSubscriberCheck forwards the router's delivery-time subscriber check to
// the inner notifier. Without the forward the wrapped Manager would deliver
// on its stored index alone.
func (a *notifierAdapter) SetSubscriberCheck(fn coresub.SubscriberCheck) {
	if s, ok := a.inner.(subscriberCheckSetter); ok {
		s.SetSubscriberCheck(fn)
		return
	}
	log.Printf("server: notifier %T takes no subscriber check; notifications are not re-checked at delivery", a.inner)
}

// AdaptNotifier wraps a handler.ResourceNotifier as an
// assembly.ResourceNotifier. Returns nil when n is nil so
// assembly.BuildProtocolRouter can skip fan-out safely (nil notifier
// is the documented "disable notification" sentinel).
//
// A typed nil (e.g. a nil *subscription.Manager stored in the interface) would
// pass the n == nil guard and panic when Notify is dispatched. The reflect
// check below rejects that case. The Kind guard is required: reflect.Value.IsNil
// panics on non-nilable kinds (struct, int, etc.), so we only call it for the
// six nilable kinds.
func AdaptNotifier(n handler.ResourceNotifier) assembly.ResourceNotifier {
	if n == nil {
		return nil
	}
	v := reflect.ValueOf(n)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		if v.IsNil() {
			return nil
		}
	}
	if _, ok := n.(notificationURIValidator); !ok {
		log.Printf("server: notifier has no notificationURI validator (%T); subscription creation applies the default DestinationPolicy", n)
	}
	return &notifierAdapter{inner: n}
}
