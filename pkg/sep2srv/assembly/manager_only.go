package assembly

import (
	"errors"
	"fmt"
	"net/http"

	coreedev "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/enddevice"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// managerOnlyWrites holds the Optional writes that change a credential or
// server-authored data. Only a manager certificate may make them, and the
// ownership gate decides it once here at registration, so a handler carries no
// check of its own. A row is added with the route that needs it; each names a
// write the WADL marks Optional.
var managerOnlyWrites = map[string]bool{
	"PUT /edev/{id}/rg":    true,
	"DELETE /edev/{id}/rg": true,
	"POST /upt":            true,

	"DELETE /upt/{uptId}":                             true,
	"DELETE /upt/{uptId}/mr/{mrId}":                   true,
	"PUT /upt/{uptId}/mr/{mrId}/rt":                   true,
	"DELETE /upt/{uptId}/mr/{mrId}/rs/{rsId}":         true,
	"PUT /upt/{uptId}/mr/{mrId}/rs/{rsId}/r/{rId}":    true,
	"DELETE /upt/{uptId}/mr/{mrId}/rs/{rsId}/r/{rId}": true,
}

// decideManagerOnly admits a manager certificate acting for a device it has a
// live pair with, and refuses everything else with 403. A route that names a
// device needs that device's current manager; a route that names none (POST
// /upt) needs at least one pair whose device exists. A device that does not
// exist is refused like one the caller does not manage, so a manager cannot
// probe which ids exist. Without an identity or a management store the answer
// cannot be established and the write stays refused. A store failure is a 500.
func (g *ownershipGate) decideManagerOnly(r *http.Request, names bool) ownershipVerdict {
	ctx := r.Context()
	caller, ok := g.caller(ctx)
	switch {
	case !ok:
		return ownershipVerdict{reason: reasonNoIdentity}
	case g.managersAbsent:
		return ownershipVerdict{caller: caller, reason: reasonNotManagerCert}
	case g.devicesAbsent:
		return ownershipVerdict{decision: ownershipStoreFailed, err: errors.New("manager-only check has no EndDevice store to read")}
	}
	if !names {
		live, err := coreedev.HasLivePair(ctx, g.devices, g.managers, caller)
		switch {
		case err != nil:
			return ownershipVerdict{decision: ownershipStoreFailed, err: fmt.Errorf("manager-only check: %w", err)}
		case !live:
			return ownershipVerdict{caller: caller, reason: reasonNotManagerCert}
		}
		return ownershipVerdict{decision: ownershipAllowed, manager: caller}
	}
	dev, err := g.devices.Get(ctx, r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ownershipVerdict{caller: caller, reason: reasonAbsent}
	case err != nil:
		return ownershipVerdict{decision: ownershipStoreFailed, err: fmt.Errorf("manager-only check could not read the EndDevice: %w", err)}
	}
	manages, err := coreedev.CurrentManagerOwns(ctx, g.managers, dev.LFDI, caller)
	switch {
	case err != nil:
		return ownershipVerdict{decision: ownershipStoreFailed, err: fmt.Errorf("manager-only check could not read the EndDevice's manager: %w", err)}
	case !manages:
		return ownershipVerdict{caller: caller, reason: reasonNotManagerCert}
	}
	return ownershipVerdict{decision: ownershipAllowed, manager: caller}
}
