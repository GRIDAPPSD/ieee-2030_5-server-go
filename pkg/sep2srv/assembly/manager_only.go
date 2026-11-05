package assembly

import (
	"context"
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
}

// callerIsManager reports whether the request's certificate is a manager, by
// coreedev.IsManager. Without an identity or a management store it is false,
// so the writes behind it stay refused when the answer cannot be established.
func (g *ownershipGate) callerIsManager(ctx context.Context) (caller string, manager bool, err error) {
	caller, ok := g.caller(ctx)
	if !ok || g.managersAbsent {
		return caller, false, nil
	}
	manager, err = coreedev.IsManager(ctx, g.managers, caller)
	return caller, manager, err
}

// decideManagerOnly admits a manager certificate and refuses everything else
// with 403, before the device is looked at, so a refused caller learns nothing
// about which devices exist. A manager acts on any device here, not only one it
// manages: these writes are fleet-wide by design. When the route names a device,
// an absent one is a 404.
func (g *ownershipGate) decideManagerOnly(r *http.Request, names bool) ownershipVerdict {
	ctx := r.Context()
	caller, manager, err := g.callerIsManager(ctx)
	switch {
	case err != nil:
		return ownershipVerdict{decision: ownershipStoreFailed, err: fmt.Errorf("manager-only check: %w", err)}
	case caller == "":
		return ownershipVerdict{reason: reasonNoIdentity}
	case !manager:
		return ownershipVerdict{caller: caller, reason: reasonNotManagerCert}
	case !names:
		return ownershipVerdict{decision: ownershipAllowed, manager: caller}
	case g.devicesAbsent:
		return ownershipVerdict{decision: ownershipStoreFailed, err: errors.New("manager-only check has no EndDevice store to read")}
	}
	switch _, err := g.devices.Get(ctx, r.PathValue("id")); {
	case errors.Is(err, store.ErrNotFound):
		return ownershipVerdict{decision: ownershipDeviceAbsent, caller: caller, reason: reasonAbsent}
	case err != nil:
		return ownershipVerdict{decision: ownershipStoreFailed, err: fmt.Errorf("manager-only check could not read the EndDevice: %w", err)}
	}
	return ownershipVerdict{decision: ownershipAllowed, manager: caller}
}
