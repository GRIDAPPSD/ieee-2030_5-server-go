package assembly

import (
	"context"
	"errors"
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
// Ownership is decided on the STORED EndDevice record: the record at {id} must
// carry the caller's LFDI. The path segment is never compared to the LFDI,
// because {id} is an opaque server-chosen index, not an identity.
type ownershipGate struct {
	next           routeRegistrar
	devices        store.EndDeviceStore
	devicesAbsent  bool
	identity       func(ctx context.Context) (lfdi, sfdi string, ok bool)
	identityAbsent bool
}

// ownershipExempt holds the only /edev patterns served without the gate.
var ownershipExempt = map[string]bool{
	// No {id} exists before registration. The handler requires an identity
	// and takes SFDI and LFDI from the certificate, never from the body.
	"POST /edev": true,
	// The collection. Its handler lists only the caller's own EndDevice.
	"GET /edev": true,
}

func newOwnershipGate(next routeRegistrar, devices store.EndDeviceStore, identity func(ctx context.Context) (lfdi, sfdi string, ok bool)) *ownershipGate {
	g := &ownershipGate{
		next:           next,
		devices:        devices,
		devicesAbsent:  store.IsAbsent(devices),
		identity:       identity,
		identityAbsent: identity == nil,
	}
	if g.devicesAbsent {
		log.Print("assembly: Stores.EndDevices is not wired: every /edev/{id} route will answer 403")
	}
	return g
}

func (g *ownershipGate) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	if requiresOwnership(pattern) {
		h = g.wrap(h)
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

type ownershipDecision int

const (
	// ownershipDenied is the zero value, so an undecided request is refused.
	ownershipDenied ownershipDecision = iota
	ownershipDeviceAbsent
	ownershipAllowed
)

func (g *ownershipGate) wrap(next func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		switch g.decide(r) {
		case ownershipAllowed:
			next(w, r)
		case ownershipDeviceAbsent:
			http.Error(w, "not found", http.StatusNotFound)
		default:
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}
}

func (g *ownershipGate) decide(r *http.Request) ownershipDecision {
	if g.identityAbsent {
		return ownershipDenied
	}
	callerLFDI, _, ok := g.identity(r.Context())
	if !ok || callerLFDI == "" {
		return ownershipDenied
	}
	id := r.PathValue("id")
	if id == "" || g.devicesAbsent {
		return ownershipDenied
	}

	dev, err := g.devices.Get(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ownershipDeviceAbsent
	case err != nil:
		// Refused rather than a 500: without the record the gate cannot say
		// yes, and the client learns nothing either way. The cause is logged.
		log.Printf("assembly: ownership check on %s could not read the EndDevice, refusing: %v", srverr.Route(r), err)
		return ownershipDenied
	case !coreedev.OwnedBy(dev.LFDI, callerLFDI):
		return ownershipDenied
	}
	return ownershipAllowed
}
