// Package sources reads the flow reservation and DER control stores and
// turns them into the values internal/commitment checks. It is the one
// place that knows how either kind of commitment is stored, so the rule
// package stays free of both.
package sources

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derhref"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/flowreservation"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// FleetResolver maps an EndDevice id to its fleet key; commitment.Resolver
// is the production one.
type FleetResolver interface {
	FleetOf(ctx context.Context, endDeviceID string) (string, error)
}

// scopedLister is the subset of a scoped store a source walks.
type scopedLister[T any] interface {
	Parents(ctx context.Context) ([]string, error)
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[T], error)
}

type lifecycleGetter interface {
	Get(ctx context.Context, parentID, id string) (dercontrol.LifecycleRecord, error)
}

type lifecycleWalker interface {
	lifecycleGetter
	Parents(ctx context.Context) ([]string, error)
}

// Grants is a commitment.GrantSource over the FlowReservationResponse store
// and the response lifecycle store that carries each response's cancel mark.
type Grants struct {
	responses  scopedLister[sep2.FlowReservationResponse]
	lifecycles lifecycleGetter
	fleets     FleetResolver
}

// NewGrants builds a Grants source.
func NewGrants(responses scopedLister[sep2.FlowReservationResponse], lifecycles lifecycleGetter, fleets FleetResolver) *Grants {
	return &Grants{responses: responses, lifecycles: lifecycles, fleets: fleets}
}

var _ commitment.GrantSource = (*Grants)(nil)

// GrantsInFleet returns every response of the fleet whose interval has a
// positive duration: a denial and a grant with no interval commit nothing.
func (g *Grants) GrantsInFleet(ctx context.Context, fleetKey string) ([]commitment.Grant, error) {
	all, err := g.all(ctx)
	if err != nil {
		return nil, err
	}
	var out []commitment.Grant
	for _, gr := range all {
		if gr.FleetKey == fleetKey && gr.Window != nil && gr.Window.Duration > 0 {
			out = append(out, gr)
		}
	}
	return out, nil
}

// Grant returns the response with this mRID, whatever its interval, so an
// execution naming an interval-less grant is refused as not executable
// rather than as absent. Only a scan that completes without finding it
// returns commitment.ErrNoGrant.
func (g *Grants) Grant(ctx context.Context, mrid string) (commitment.Grant, error) {
	all, err := g.all(ctx)
	if err != nil {
		return commitment.Grant{}, err
	}
	for _, gr := range all {
		if gr.MRID == mrid {
			return gr, nil
		}
	}
	return commitment.Grant{}, fmt.Errorf("sources: grant %s: %w", mrid, commitment.ErrNoGrant)
}

func (g *Grants) all(ctx context.Context) ([]commitment.Grant, error) {
	parents, err := g.responses.Parents(ctx)
	if err != nil {
		return nil, fmt.Errorf("sources: listing response parents: %w", err)
	}
	var out []commitment.Grant
	for _, edevID := range parents {
		page, err := g.responses.List(ctx, edevID, store.ListOptions{Unbounded: true})
		if err != nil {
			return nil, fmt.Errorf("sources: listing responses of %s: %w", edevID, err)
		}
		if len(page.Items) == 0 {
			continue
		}
		fleet, err := g.fleets.FleetOf(ctx, edevID)
		if err != nil {
			return nil, err
		}
		for _, frp := range page.Items {
			gr, err := g.grantOf(ctx, edevID, fleet, frp)
			if err != nil {
				return nil, err
			}
			out = append(out, gr)
		}
	}
	return out, nil
}

func (g *Grants) grantOf(ctx context.Context, edevID, fleet string, frp sep2.FlowReservationResponse) (commitment.Grant, error) {
	id, ok := flowreservation.ResponseID(edevID, frp.Href)
	if !ok {
		return commitment.Grant{}, fmt.Errorf("sources: response href %q under %s has no store id", frp.Href, edevID)
	}
	gr := commitment.Grant{
		MRID:        frp.MRID,
		EndDeviceID: edevID,
		FleetKey:    fleet,
		Energy:      frp.EnergyAvailable,
		Power:       frp.PowerAvailable,
	}
	if frp.Interval != nil {
		gr.Window = &commitment.Window{Start: frp.Interval.Start, Duration: frp.Interval.Duration}
	}
	lc, err := g.lifecycles.Get(ctx, edevID, id)
	switch {
	case err == nil:
		gr.CancelledAt = lc.CancelledAt
	case !errors.Is(err, store.ErrNotFound):
		return commitment.Grant{}, fmt.Errorf("sources: lifecycle of response %s/%s: %w", edevID, id, err)
	}
	return gr, nil
}

// Controls is a commitment.ControlSource over the DER control store and its
// lifecycle store. Only controls with a lifecycle record are returned: boot
// fixture and CSIP loader controls have none and are never counted.
type Controls struct {
	controls   scopedLister[sep2.DERControl]
	lifecycles lifecycleWalker
	fleets     FleetResolver

	logf    func(format string, args ...any)
	orphans sync.Map // EndDevice ids already logged as gone
}

// NewControls builds a Controls source.
func NewControls(controls scopedLister[sep2.DERControl], lifecycles lifecycleWalker, fleets FleetResolver) *Controls {
	return &Controls{controls: controls, lifecycles: lifecycles, fleets: fleets, logf: log.Printf}
}

var _ commitment.ControlSource = (*Controls)(nil)

// ControlsInFleet returns every issuer-created control of the fleet,
// cancelled or not: liveness is the ledger's test.
func (c *Controls) ControlsInFleet(ctx context.Context, fleetKey string) ([]commitment.Control, error) {
	return c.filter(ctx, func(ctl commitment.Control) bool { return ctl.FleetKey == fleetKey })
}

// ExecutionsOf returns every issuer-created control linked to grantMRID. An
// empty mRID is refused: it would match every plain dispatch.
func (c *Controls) ExecutionsOf(ctx context.Context, grantMRID string) ([]commitment.Control, error) {
	if grantMRID == "" {
		return nil, errors.New("sources: ExecutionsOf needs a grant mRID")
	}
	return c.filter(ctx, func(ctl commitment.Control) bool { return ctl.GrantMRID == grantMRID })
}

func (c *Controls) filter(ctx context.Context, keep func(commitment.Control) bool) ([]commitment.Control, error) {
	scopes, err := c.lifecycles.Parents(ctx)
	if err != nil {
		return nil, fmt.Errorf("sources: listing control lifecycle scopes: %w", err)
	}
	fleetOf := map[string]string{} // "" marks an EndDevice that is gone
	var out []commitment.Control
	for _, scope := range scopes {
		edevID, ok := scopeEndDevice(scope)
		if !ok {
			return nil, fmt.Errorf("sources: control scope %q is not edev/fsa/derp", scope)
		}
		page, err := c.controls.List(ctx, scope, store.ListOptions{Unbounded: true})
		if err != nil {
			return nil, fmt.Errorf("sources: listing controls of %s: %w", scope, err)
		}
		for _, ctrl := range page.Items {
			id, ok := derhref.ControlID(ctrl.Href)
			if !ok {
				return nil, fmt.Errorf("sources: control href %q in %s has no store id", ctrl.Href, scope)
			}
			lc, err := c.lifecycles.Get(ctx, scope, id)
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("sources: lifecycle of control %s/%s: %w", scope, id, err)
			}
			fleet, cached := fleetOf[edevID]
			if !cached {
				fleet, err = c.resolve(ctx, edevID)
				if err != nil {
					return nil, err
				}
				fleetOf[edevID] = fleet
			}
			if fleet == "" {
				continue
			}
			ctl, err := controlOf(scope, fleet, ctrl, lc)
			if err != nil {
				return nil, err
			}
			if keep(ctl) {
				out = append(out, ctl)
			}
		}
	}
	return out, nil
}

// resolve returns the fleet of a control scope's EndDevice, or "" when the
// device is gone. DER controls outlive a DELETE of their EndDevice (#721),
// and no device reads them afterwards, so such a scope belongs to no live
// fleet; refusing instead would block every fleet's checks on one orphan.
// Any other failure still refuses.
func (c *Controls) resolve(ctx context.Context, edevID string) (string, error) {
	fleet, err := c.fleets.FleetOf(ctx, edevID)
	if err == nil {
		return fleet, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if _, logged := c.orphans.LoadOrStore(edevID, true); !logged {
		c.logf("commitment sources: DER controls under EndDevice %s outlive their device; not counted in any fleet: %v", edevID, err)
	}
	return "", nil
}

// controlOf builds the ledger's view of one control. The lifecycle record
// carries no link yet (#714), so every control reads as a plain dispatch
// reaching the one device it is stored under.
func controlOf(scope, fleet string, ctrl sep2.DERControl, lc dercontrol.LifecycleRecord) (commitment.Control, error) {
	if ctrl.Interval == nil {
		return commitment.Control{}, fmt.Errorf("sources: control %s in %s has no interval", ctrl.MRID, scope)
	}
	ctl := commitment.Control{
		MRID:      ctrl.MRID,
		Scope:     scope,
		FleetKey:  fleet,
		Window:    commitment.Window{Start: ctrl.Interval.Start, Duration: ctrl.Interval.Duration},
		Reach:     1,
		Cancelled: lc.CancelledAt != nil,
	}
	if ctrl.DERControlBase != nil {
		ctl.TargetW = ctrl.DERControlBase.OpModTargetW
	}
	if lc.SupersededAt != nil {
		ctl.Window = ctl.Window.ClipAt(*lc.SupersededAt)
	}
	return ctl, nil
}

func scopeEndDevice(scope string) (string, bool) {
	parts := strings.Split(scope, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	return parts[0], true
}

// NewLedger builds the commitment ledger over the stores it checks. A
// process builds exactly one: two would hold two lock maps and serialize
// nothing between them.
func NewLedger(
	devices interface {
		Get(ctx context.Context, id string) (sep2.EndDevice, error)
	},
	managers store.EndDeviceManagementReader,
	responses scopedLister[sep2.FlowReservationResponse],
	responseLifecycles lifecycleGetter,
	controls scopedLister[sep2.DERControl],
	controlLifecycles lifecycleWalker,
) *commitment.Ledger {
	fleets := commitment.Resolver{Devices: devices, Managers: managers}
	return commitment.NewLedger(
		NewGrants(responses, responseLifecycles, fleets),
		NewControls(controls, controlLifecycles, fleets),
	)
}
