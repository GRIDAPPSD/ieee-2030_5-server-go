package adminplane

import (
	"context"
	"errors"
	"log/slog"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/fsaprogram"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// #163 wiring: build the *handler.AdminFSAHandler from the server's
// stores plus a tiny adapter that turns the existing DERProgram scoped
// store into a DERProgramHrefValidator (the program href shape is
// `/edev/{id}/fsa/{fsaId}/derp/{derpId}`: we parse to (edev, fsa, derp) and
// probe the store).

// derProgramHrefValidator adapts a scoped DERProgram store to
// handler.DERProgramHrefValidator. It is defined at the consumer (server
// package) per the Pike rule rather than baked into the handler package.
//
// programs is declared as the store.ScopedStore contract rather than a
// concrete *memory.ScopedStore. The validator needs Get and nothing else,
// and core made *memory.DERProgramStore hold its collection instead of
// embedding it, so there is no longer a concrete inner store to reach
// for. Holding the contract is the shape that change was for.
type derProgramHrefValidator struct {
	programs store.ScopedStore[sep2.DERProgram]
}

// HasProgram returns true if href resolves to an existing DERProgram.
// Spec-shape href: /edev/{id}/fsa/{fsaId}/derp/{derpId}. Anything else
// falls through as "not found": the admin endpoint will surface 404. The
// FSA segment must be the one the stored program's own href names, since
// the protocol route serves a program under that FSA only (#916).
func (v *derProgramHrefValidator) HasProgram(ctx context.Context, href string) bool {
	if v == nil || v.programs == nil {
		return false
	}
	edevID, fsaID, derpID, ok := derhref.Program(href)
	if !ok {
		return false
	}
	prog, err := v.programs.Get(ctx, edevID, derpID)
	if err != nil {
		return false
	}
	_, ownFSA, _, ok := derhref.Program(prog.Href)
	return ok && ownFSA == fsaID
}

// newAdminFSAHandler builds the handler from the server's stores. Returns
// nil when AdminFSAs is unset so the caller can decide to skip route
// registration entirely.
func newAdminFSAHandler(stores *Stores) *handler.AdminFSAHandler {
	if stores == nil || stores.AdminFSAs == nil {
		return nil
	}
	h := &handler.AdminFSAHandler{
		AdminFSAs:   stores.AdminFSAs,
		DeviceFSAs:  stores.FSAs,
		EndDevices:  stores.EndDevices,
		DERPrograms: &derProgramHrefValidator{programs: stores.DERPrograms},
		Copies:      templateCopies(stores),
		Notifier:    stores.AdminNotifier,
		PEN:         stores.PEN,
		Retirer:     programRetirer(stores),
		Curves:      curveReader(stores),
	}
	if !store.IsAbsent(stores.EndDeviceManagers) && stores.EndDevices != nil {
		h.Managers = stores.EndDeviceManagers
		h.FleetDevices = stores.EndDevices
	}
	return h
}

// programRetirer is the process's DER control issuer, so a program delete
// and a control create lock the same scopes. A Stores built without one gets
// its own, which only tests do; nil (every delete answers 500) when the
// control stores are unset.
func programRetirer(stores *Stores) handler.ProgramRetirer {
	if stores.DERControlIssuer != nil {
		return stores.DERControlIssuer
	}
	if store.IsAbsent(stores.DERPrograms) || stores.DERControls == nil || store.IsAbsent(stores.DERControlLifecycles) {
		return nil
	}
	issuer, err := assembly.NewDERControlIssuer(stores.DERPrograms, stores.DERControls, stores.DERControlLifecycles, stores.PEN)
	if err != nil {
		slog.Error("admin: no DER control issuer for program deletes; every program delete answers 500", "event", "program_retirer_unavailable", "error", err)
		return nil
	}
	return issuer
}

// templateCopies is the store set a program template is copied into, or nil
// when any of them is unset, which leaves the template routes unmounted.
func templateCopies(stores *Stores) *fsaprogram.Copies {
	if store.IsAbsent(stores.DERPrograms) || store.IsAbsent(stores.DefaultDERControls) || stores.ProgramOrigins == nil {
		return nil
	}
	return &fsaprogram.Copies{
		Programs: stores.DERPrograms,
		Defaults: stores.DefaultDERControls,
		Origins:  stores.ProgramOrigins,
	}
}

// newAdminProgramListHandler builds the GET /api/der-programs handler, or
// returns nil when the program stores are not wired, which leaves the route
// unmounted. The DERProgram store must enumerate its devices.
func newAdminProgramListHandler(stores *Stores) *handler.AdminProgramListHandler {
	if stores == nil || store.IsAbsent(stores.DERPrograms) || stores.ProgramOrigins == nil {
		return nil
	}
	programs, ok := stores.DERPrograms.(handler.ProgramLister)
	if !ok {
		slog.Warn("admin: GET /api/der-programs not mounted: the DERProgram store cannot list its devices", "event", "der_program_list_unmounted")
		return nil
	}
	h := &handler.AdminProgramListHandler{Programs: programs, Origins: stores.ProgramOrigins}
	if p, ok := stores.DERPrograms.(handler.Persister); ok {
		h.Persist = p
	} else {
		slog.Warn("admin: DER program store does not report persistence; GET /api/der-programs reports every program as not surviving a restart", "event", "der_program_list_persists_unknown")
	}
	return h
}

// newAdminFSAControlHandler builds the FSA control handler over the FSA and
// DER control handlers, and wires it as the FSA handler's replicator and the
// control handler's finder of FSA controls. Nil, mounting no routes, when
// either handler or the template stores are missing, or when the control
// handler's issuer cannot write across scopes.
func newAdminFSAControlHandler(fsaH *handler.AdminFSAHandler, derH *handler.AdminDERControlHandler) *handler.AdminFSAControlHandler {
	if fsaH == nil || fsaH.Copies == nil || derH == nil {
		return nil
	}
	issuer, ok := derH.Issuer.(handler.FSAControlIssuer)
	if !ok {
		slog.Error("admin: FSA control routes not mounted: the DER control issuer cannot write across scopes", "event", "fsa_control_unmounted")
		return nil
	}
	h := &handler.AdminFSAControlHandler{FSAs: fsaH, Controls: derH, Issuer: issuer}
	fsaH.Replicas = h
	derH.FSAControls = fsaH.AdminFSAs
	return h
}

// CompleteFSAControlReplicas writes every live FSA control to each assigned
// device's copy that lacks it: the boot rebuild of a fan-out or an assignment
// that stopped before its last copy. It runs after the template copies are
// rebuilt, and skips a copy that rebuild skipped. Every per-device failure is
// logged and skipped, as the copy rebuild does, so a device that cannot take
// its controls never stops the boot and the admin API can reach it.
func CompleteFSAControlReplicas(ctx context.Context, stores *Stores, logf func(format string, args ...any)) error {
	h := newAdminFSAControlHandler(newAdminFSAHandler(stores), newAdminDERControlHandler(stores))
	if h == nil {
		return nil
	}
	for _, fsaID := range stores.AdminFSAs.IDs(ctx) {
		for _, dev := range stores.AdminFSAs.Devices(ctx, fsaID) {
			err := h.ReplicateTo(ctx, dev, fsaID)
			var conflict *commitment.ConflictError
			switch {
			case err == nil:
			case errors.As(err, &conflict):
				logf("FSA %q controls not replicated to device %q: %s conflicts with %s", fsaID, dev, conflict.Code, conflict.MRID)
			default:
				logf("FSA %q controls not replicated to device %q: %v", fsaID, dev, err)
			}
		}
	}
	return nil
}
