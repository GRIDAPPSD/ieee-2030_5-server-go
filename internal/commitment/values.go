package commitment

import (
	"fmt"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// Grant is a live-or-not FlowReservationResponse as the ledger sees it.
type Grant struct {
	MRID        string
	EndDeviceID string // the {id} it is stored under
	FleetKey    string // resolved at read time from EndDeviceID
	Window      *Window
	Energy      *sep2.SignedRealEnergy // as stored: charging positive
	Power       *sep2.ActivePower      // magnitude is the bound; its sign is ignored
	CancelledAt *int64
}

// Control is an admin-issued DERControl as the ledger sees it.
type Control struct {
	MRID      string
	Scope     string // edev/fsa/derp
	FleetKey  string
	Window    Window // already clipped at SupersededAt, see design 5.2
	GrantMRID string // "" for a plain dispatch
	TargetW   *sep2.ActivePower
	Reach     int // managed devices of the fleet that read it
	Cancelled bool
}

// ConflictCode names the bound a ConflictError refuses on. It is a fixed
// string, never request text, because it becomes part of a 409 body.
type ConflictCode string

// The closed set of conflict codes. Not every code is produced by S1's
// FitsGrant: ConflictFleetWindow is the plain-dispatch rule (design 5.4),
// and ConflictOverlap is reserved for the literal no-overlap reading the
// operator did not choose (design 9.2).
const (
	ConflictFleetWindow     ConflictCode = "fleet_window_committed"
	ConflictGrantNotLive    ConflictCode = "grant_not_live"
	ConflictNotExecutable   ConflictCode = "grant_not_executable"
	ConflictModeNotTarget   ConflictCode = "execution_mode_not_target_w"
	ConflictOutsideFleet    ConflictCode = "execution_outside_fleet"
	ConflictOutsideInterval ConflictCode = "execution_outside_interval"
	ConflictDirection       ConflictCode = "execution_reverses_grant"
	ConflictPower           ConflictCode = "execution_exceeds_power"
	ConflictEnergy          ConflictCode = "execution_exceeds_energy"
	ConflictOverlap         ConflictCode = "execution_overlaps_execution"
)

// ConflictError is the one refusal type a check in this package returns.
// MRID names the conflicting grant or control (a 409 body names it); Code
// names the bound that was broken.
type ConflictError struct {
	Code ConflictCode
	MRID string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("commitment: %s: %s", e.Code, e.MRID)
}
