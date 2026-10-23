package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// GuardEndDeviceDelete runs del for DELETE /api/devices/{id} only while none
// of the device's DER control scopes holds a live control, through the same
// Retire the program deletes use (#1120). Retire keeps a control from landing
// until del returns. The protocol DELETE /edev/{id} is not guarded: a device
// removing itself is MAINT-002, and a refusal there would change that wire
// behavior.
//
// A nil programs store means no controls exist to protect, so del runs. A
// device with programs and a nil retirer fails closed with 500.
func GuardEndDeviceDelete(programs store.ScopedStore[sep2.DERProgram], retirer ProgramRetirer, del http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		scopes, err := deviceControlScopes(r.Context(), programs, id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "device delete: "+err.Error())
			return
		}
		if len(scopes) == 0 {
			del(w, r)
			return
		}
		if retirer == nil {
			writeError(w, http.StatusInternalServerError, "device delete: no DER control issuer, so live controls cannot be checked")
			return
		}
		err = retirer.Retire(r.Context(), scopes, func(context.Context) error {
			del(w, r)
			return nil
		})
		var live *dercontrol.LiveControlsError
		switch {
		case errors.As(err, &live):
			slog.WarnContext(r.Context(), "admin: device delete refused", "event", "device_delete_refused",
				"code", "live_controls", "device", logsafe.Str(id), "controls", logsafe.Str(strings.Join(live.Controls, ",")))
			writeJSON(w, http.StatusConflict, programRefusal{
				Error:    "cancel these controls, or wait for them to end, before deleting the device",
				Code:     "live_controls",
				Controls: slices.Clone(live.Controls),
			})
		case err != nil:
			writeError(w, http.StatusInternalServerError, "device delete: "+err.Error())
		}
	}
}

// deviceControlScopes lists the control scopes of every program the device
// holds. The issuer files controls under the FSA a program's control list
// link names, which a fixture program may set apart from its href, so both
// are scopes.
func deviceControlScopes(ctx context.Context, programs store.ScopedStore[sep2.DERProgram], deviceID string) ([]dercontrol.Scope, error) {
	if store.IsAbsent(programs) {
		return nil, nil
	}
	list, err := programs.List(ctx, deviceID, store.ListOptions{Unbounded: true})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var scopes []dercontrol.Scope
	for _, p := range list.Items {
		edev, fsaID, derp, ok := derhref.Program(p.Href)
		if !ok || edev != deviceID {
			continue
		}
		scopes = append(scopes, dercontrol.Scope{EndDeviceID: deviceID, FSAID: fsaID, DERProgramID: derp})
		if p.DERControlListLink != nil {
			if _, linkFSA, _, ok := derhref.ControlList(p.DERControlListLink.Href); ok && linkFSA != fsaID {
				scopes = append(scopes, dercontrol.Scope{EndDeviceID: deviceID, FSAID: linkFSA, DERProgramID: derp})
			}
		}
	}
	return scopes, nil
}
