package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/logsafe"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// GuardEndDeviceDelete runs del for DELETE /api/devices/{id} only while none
// of the device's DER control scopes holds a live control, through the same
// Retire the program deletes use (#1120). It holds programMu from the program
// listing through del, as every other program writer does, so an assignment
// cannot copy a live control onto the device between the check and the
// delete; Retire keeps a control from landing meanwhile. The protocol
// DELETE /edev/{id} is not guarded: a device removing itself is MAINT-002,
// and a refusal there would change that wire behavior.
//
// A nil programs store means no controls exist to protect, so del runs. A
// device with programs and a nil Retirer fails closed with 500, as does a
// program whose href names no program of the device.
func (h *AdminFSAHandler) GuardEndDeviceDelete(programs store.ScopedStore[sep2.DERProgram], del http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		h.programMu.Lock()
		defer h.programMu.Unlock()

		scopes, err := deviceControlScopes(r.Context(), programs, id)
		var unaddressable *unaddressableProgramError
		switch {
		case errors.As(err, &unaddressable):
			refuseProgram(w, "unaddressable_program", "a program of this device has an href that names no FSA of this device")
			return
		case err != nil:
			h.templateFailed(w, r, "device_delete_scopes", err)
			return
		}
		if len(scopes) == 0 {
			del(w, r)
			return
		}
		if h.Retirer == nil {
			h.templateFailed(w, r, "device_delete_no_retirer", errors.New("no DER control issuer, so live controls cannot be checked"))
			return
		}
		err = h.Retirer.Retire(r.Context(), scopes, func(context.Context) error {
			del(w, r)
			return nil
		})
		var live *dercontrol.LiveControlsError
		switch {
		case errors.As(err, &live):
			slog.WarnContext(r.Context(), "admin: device delete refused", "event", "device_delete_refused",
				"code", "live_controls", "device", logsafe.Str(id), "controls", logsafe.Str(strings.Join(live.Controls, ",")))
			writeLiveControlsRefusal(w, live, "cancel these controls, or wait for them to end, before deleting the device")
		case err != nil:
			h.templateFailed(w, r, "device_delete_retire", err)
		}
	}
}

// unaddressableProgramError names a program whose href names no program of
// the device, the case lookupDeviceProgram refuses as unaddressable_program.
type unaddressableProgramError struct{ href string }

func (e *unaddressableProgramError) Error() string {
	return "program href " + e.href + " names no program of the device"
}

// deviceControlScopes lists the control scopes of every program the device
// holds, through programScopes.
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
		s, _, _, ok := programScopes(p, deviceID)
		if !ok {
			return nil, &unaddressableProgramError{href: p.Href}
		}
		scopes = append(scopes, s...)
	}
	return scopes, nil
}
