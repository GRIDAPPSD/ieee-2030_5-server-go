// Package fsaprogram builds the device-scoped FSA that /edev/{id}/fsa serves
// from an admin FSA, both when a device is assigned and when the server
// restarts.
package fsaprogram

import (
	"context"
	"errors"
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// DeviceFSAWriter is the slice of the device-scoped FSA store Reconcile writes.
type DeviceFSAWriter interface {
	Create(ctx context.Context, parentID, id string, resource sep2.FunctionSetAssignments) error
	Delete(ctx context.Context, parentID, id string) error
}

// Scoped returns the FSA served under the device's edev tree for an admin
// FSA. Assignment and Reconcile share it so a rebuilt record is byte-identical
// to the one written at assignment.
func Scoped(deviceID, fsaID string, admin sep2.FunctionSetAssignments) sep2.FunctionSetAssignments {
	href := fmt.Sprintf("/edev/%s/fsa/%s", deviceID, fsaID)
	return sep2.FunctionSetAssignments{
		Resource:           sep2.Resource{Href: href},
		MRID:               admin.MRID,
		Description:        admin.Description,
		DERProgramListLink: &sep2.ListLink{Href: href + "/derp"},
	}
}

// Reconcile re-materializes every recorded device assignment into the
// device-scoped store, which is in-memory only. A record already present for
// the same device and id is replaced, because the admin record is the one an
// operator last changed. An admin store with no assignments is a no-op.
func Reconcile(ctx context.Context, admin *memory.AdminFSAStore, devices DeviceFSAWriter) error {
	for _, fsaID := range admin.IDs(ctx) {
		fsa, err := admin.Get(ctx, fsaID)
		if err != nil {
			return fmt.Errorf("rebuild FSA %q: %w", fsaID, err)
		}
		for _, deviceID := range admin.Devices(ctx, fsaID) {
			scoped := Scoped(deviceID, fsaID, fsa)
			err := devices.Create(ctx, deviceID, fsaID, scoped)
			if errors.Is(err, store.ErrAlreadyExists) {
				if err = devices.Delete(ctx, deviceID, fsaID); err == nil {
					err = devices.Create(ctx, deviceID, fsaID, scoped)
				}
			}
			if err != nil {
				return fmt.Errorf("rebuild FSA %q for device %q: %w", fsaID, deviceID, err)
			}
		}
	}
	return nil
}
