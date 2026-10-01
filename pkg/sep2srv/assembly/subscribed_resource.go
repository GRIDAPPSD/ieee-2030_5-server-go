package assembly

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// A Subscription is accepted, and later delivered, only when its subscriber
// could GET the subscribedResource: the href must name a route this gate
// decides, and the gate's decision for that route must admit the subscriber.
// A resource served by a route outside /edev is refused, because the access
// rules of those routes live in their handlers rather than in the gate.

// readRoute is what the gate decided about one GET pattern at registration.
type readRoute struct {
	gated     bool
	delegated bool
}

type readMatchKey struct{}

// readMatch is filled in by the probe the reads mux dispatches to.
type readMatch struct {
	found bool
	route readRoute
	id    string
}

func readProbe(route readRoute) func(http.ResponseWriter, *http.Request) {
	return func(_ http.ResponseWriter, r *http.Request) {
		if m, ok := r.Context().Value(readMatchKey{}).(*readMatch); ok {
			m.found, m.route, m.id = true, route, r.PathValue("id")
		}
	}
}

// servesGET reports whether pattern answers a GET: its method is GET, or it
// names no method at all.
func servesGET(pattern string) bool {
	i := strings.IndexByte(pattern, '/')
	if i < 0 {
		return false
	}
	method, _, _ := strings.Cut(pattern[:i], " ")
	return method == "" || method == http.MethodGet
}

// discardResponse absorbs whatever the reads mux writes for an href no probe
// matched (a not-found or redirect answer).
type discardResponse struct{}

func (discardResponse) Header() http.Header         { return http.Header{} }
func (discardResponse) Write(b []byte) (int, error) { return len(b), nil }
func (discardResponse) WriteHeader(int)             {}

func refused(format string, args ...any) error {
	return fmt.Errorf("%w: %s", coresub.ErrSubscribedResourceRefused, fmt.Sprintf(format, args...))
}

// canonicalResourcePath accepts only a server-relative path already in the
// form path.Clean gives it, so the href that is checked, stored and later
// matched against a notified resource is one and the same string.
func canonicalResourcePath(href string) (string, error) {
	if href == "" {
		return "", errors.New("empty")
	}
	if strings.ContainsAny(href, "?#%\\") {
		return "", errors.New("carries a query, fragment, escape or backslash")
	}
	if !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
		return "", errors.New("not a server-relative path")
	}
	u, err := url.Parse(href)
	if err != nil {
		return "", errors.New("not a valid path")
	}
	if u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "", errors.New("not a server-relative path")
	}
	if path.Clean(href) != href {
		return "", errors.New("not in canonical form")
	}
	return href, nil
}

// canRead reports whether the caller may GET href, under the decision the
// gate applies to a GET request for it. caller is consulted only when the
// matched route is gated, so a route any authenticated device may read, such
// as the EndDevice collection, does not depend on the subscriber's record.
func (g *ownershipGate) canRead(ctx context.Context, href string, caller func() (string, error)) error {
	p, err := canonicalResourcePath(href)
	if err != nil {
		return refused("%v", err)
	}
	match := &readMatch{}
	probe := (&http.Request{Method: http.MethodGet, URL: &url.URL{Path: p}, Header: http.Header{}}).
		WithContext(context.WithValue(ctx, readMatchKey{}, match))
	g.reads.ServeHTTP(discardResponse{}, probe)
	if !match.found {
		return refused("no readable route")
	}
	if !match.route.gated {
		return nil
	}
	callerLFDI, err := caller()
	if err != nil {
		return err
	}
	v := g.decideFor(ctx, callerLFDI, match.id, match.route.delegated)
	switch v.decision {
	case ownershipAllowed:
		return nil
	case ownershipStoreFailed:
		return v.err
	case ownershipDeviceAbsent:
		return refused("%s", reasonAbsent)
	default:
		return refused("%s", v.reason)
	}
}

// checkSubscribe is the create-time coresub.ReadCheck: the caller is the
// request's authenticated identity.
func (g *ownershipGate) checkSubscribe(r *http.Request, resource string) error {
	return g.canRead(r.Context(), resource, func() (string, error) {
		callerLFDI, ok := g.caller(r.Context())
		if !ok {
			return "", refused("%s", reasonNoIdentity)
		}
		return callerLFDI, nil
	})
}

// checkSubscriber is the delivery-time coresub.SubscriberCheck: the
// subscriber is the device owning the EndDevice the subscription was created
// under, read from the store at delivery so a changed owner or manager counts.
func (g *ownershipGate) checkSubscriber(ctx context.Context, sub sep2.Subscription) error {
	return g.canRead(ctx, sub.SubscribedResource, func() (string, error) {
		return g.subscriberLFDI(ctx, sub.Href)
	})
}

func (g *ownershipGate) subscriberLFDI(ctx context.Context, subHref string) (string, error) {
	edevID, ok := coresub.SubscriberEndDevice(subHref)
	if !ok {
		return "", refused("subscription href names no EndDevice")
	}
	if g.devicesAbsent {
		return "", errors.New("subscriber check has no EndDevice store to read")
	}
	dev, err := g.devices.Get(ctx, edevID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return "", refused("subscriber EndDevice absent")
	case err != nil:
		return "", fmt.Errorf("subscriber check could not read the EndDevice: %w", err)
	case dev.LFDI == "":
		return "", refused("%s", reasonRecordHasNoLFDI)
	}
	return dev.LFDI, nil
}
