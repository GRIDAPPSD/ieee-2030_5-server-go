package assembly

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	coreedev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/enddevice"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/srverr"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// ownershipGate is a routeRegistrar that binds path {id} to the authenticated
// caller on every route under /edev except the two in ownershipExempt.
//
// It gates at registration, from the pattern string, so a route added to any
// register*Routes helper later is covered without its author doing anything,
// and handlers that know nothing about EndDevices stay that way. The request
// is checked after the mux matched, through r.PathValue, so the request path
// is never re-parsed.
//
// Access is decided on the STORED EndDevice record, never on the path segment,
// because {id} is an opaque server-chosen index, not an identity. The caller
// is admitted when the record carries the caller's LFDI, or, on a delegable
// pattern, when the management store names the caller as the record's
// manager.
type ownershipGate struct {
	next           routeRegistrar
	devices        store.EndDeviceStore
	devicesAbsent  bool
	managers       store.EndDeviceManagementStore
	managersAbsent bool
	identity       func(ctx context.Context) (lfdi, sfdi string, ok bool)
	identityAbsent bool
}

// ownershipExempt holds the only /edev patterns served without the gate.
var ownershipExempt = map[string]bool{
	// No {id} exists before registration. The handler requires an identity
	// and takes SFDI and LFDI from the certificate, never from the body.
	"POST /edev": true,
	// The collection. Its handler lists only the caller's own EndDevice and
	// those it manages.
	"GET /edev": true,
}

func newOwnershipGate(next routeRegistrar, devices store.EndDeviceStore, managers store.EndDeviceManagementStore, identity func(ctx context.Context) (lfdi, sfdi string, ok bool)) *ownershipGate {
	g := &ownershipGate{
		next:           next,
		devices:        devices,
		devicesAbsent:  store.IsAbsent(devices),
		managers:       managers,
		managersAbsent: store.IsAbsent(managers),
		identity:       identity,
		identityAbsent: identity == nil,
	}
	if g.devicesAbsent {
		log.Print("assembly: Stores.EndDevices is not wired: every /edev/{id} route will answer 403")
	}
	if g.managersAbsent {
		log.Print("assembly: Stores.EndDeviceManagers is not wired: no EndDevice access is delegated to a manager")
	}
	return g
}

func (g *ownershipGate) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	if requiresOwnership(pattern) {
		h = g.wrap(h, delegable(pattern))
	}
	g.next.HandleFunc(pattern, h)
}

// requiresOwnership reports whether pattern is under /edev and not exempt.
// A gated pattern that names no {id} wildcard reads an empty id and is
// refused, which is the safe answer for a route nobody has reasoned about.
func requiresOwnership(pattern string) bool {
	if ownershipExempt[pattern] {
		return false
	}
	i := strings.IndexByte(pattern, '/')
	if i < 0 {
		return false
	}
	path := pattern[i:]
	return path == "/edev" || strings.HasPrefix(path, "/edev/")
}

// delegable reports whether a manager may use pattern on a device it manages:
// GET on the record, which also serves HEAD, and every pattern strictly below
// it except the Registration. A manager never rewrites or deletes the record
// itself, and a record pattern that names no method is not delegated.
func delegable(pattern string) bool {
	i := strings.IndexByte(pattern, '/')
	if i < 0 {
		return false
	}
	method, _, _ := strings.Cut(pattern[:i], " ")
	segments := strings.Split(pattern[i+1:], "/")
	if len(segments) < 2 || segments[0] != "edev" || segments[1] != "{id}" {
		return false
	}
	if len(segments) == 2 {
		return method == http.MethodGet
	}
	// The resource segment must be a literal other than the Registration: a
	// wildcard or an empty segment there would also match the Registration.
	resource := segments[2]
	return resource != "" && resource != "rg" && !strings.HasPrefix(resource, "{")
}

type ownershipDecision int

const (
	// ownershipDenied is the zero value, so an undecided request is refused.
	ownershipDenied ownershipDecision = iota
	ownershipDeviceAbsent
	ownershipStoreFailed
	ownershipAllowed
)

func (g *ownershipGate) wrap(next func(http.ResponseWriter, *http.Request), delegated bool) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		decision, err := g.decide(r, delegated)
		switch decision {
		case ownershipAllowed:
			next(w, r)
		case ownershipDeviceAbsent:
			http.Error(w, "not found", http.StatusNotFound)
		case ownershipStoreFailed:
			// A 500, not a 403: the gate could not establish access, and a
			// 403 would tell the client it is not authorized. The handler
			// still never runs.
			srverr.Internal(w, r, err)
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}
}

func (g *ownershipGate) decide(r *http.Request, delegated bool) (ownershipDecision, error) {
	if g.identityAbsent {
		return ownershipDenied, nil
	}
	callerLFDI, _, ok := g.identity(r.Context())
	if !ok || callerLFDI == "" {
		return ownershipDenied, nil
	}
	id := r.PathValue("id")
	if id == "" || g.devicesAbsent {
		return ownershipDenied, nil
	}

	dev, err := g.devices.Get(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ownershipDeviceAbsent, nil
	case err != nil:
		return ownershipStoreFailed, fmt.Errorf("ownership check could not read the EndDevice: %w", err)
	case coreedev.OwnedBy(dev.LFDI, callerLFDI):
		return ownershipAllowed, nil
	}

	// Management is consulted only after self fails and only on a delegable
	// pattern, so a management store outage never blocks a device's own access.
	if !delegated || g.managersAbsent || dev.LFDI == "" {
		return ownershipDenied, nil
	}
	manager, err := g.managers.ManagerOf(r.Context(), dev.LFDI)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ownershipDenied, nil
	case err != nil:
		return ownershipStoreFailed, fmt.Errorf("ownership check could not read the EndDevice's manager: %w", err)
	case !coreedev.OwnedBy(manager, callerLFDI):
		return ownershipDenied, nil
	}
	return ownershipAllowed, nil
}
