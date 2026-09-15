// Package dercontrol turns an operator request into a conformant IEEE
// 2030.5 DERControl event and keeps that control's lifecycle (supersede
// and cancel). It has no HTTP route: an admin handler (a later issue)
// calls it and maps its errors to wire responses.
package dercontrol

import (
	"context"
	"fmt"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// Default bounds applied by NewIssuer when a Config field is the zero
// value. There is no default PEN: an unset PEN always refuses (RefusalPENNotConfigured).
const (
	DefaultStartLead   = 7 * 24 * time.Hour
	DefaultMinDuration = 60 * time.Second
	DefaultMaxDuration = 24 * time.Hour
)

// ControlType selects which DERControlBase shape Issue builds. The set is
// closed: IEEE 2030.5-2018 defines other opMod fields, but v1 supports only
// these four (design decision D1/D2, der-control-route-design-2026-09-14.md
// section 4).
type ControlType string

const (
	Connect        ControlType = "connect"
	Disconnect     ControlType = "disconnect"
	MaxLimW        ControlType = "maxLimW"
	FixedPFInjectW ControlType = "fixedPFInjectW"
)

// PowerFactorValue is the value object required for FixedPFInjectW.
// Displacement is thousandths of cos(theta) (1 to 1000); Excitation is
// true when the DER absorbs reactive power (under-excited), false when it
// injects (over-excited). IEEE 2030.5-2018 lines 16516-16522.
type PowerFactorValue struct {
	Displacement uint16
	Excitation   bool
}

// CreateRequest is the operator's input. The issuer owns every DERControl
// field not listed here: mRID, creationTime, EventStatus, replyTo,
// responseRequired, randomizeStart and randomizeDuration are never taken
// from a request (acceptance criterion 5).
type CreateRequest struct {
	// DERProgramHref is "/edev/{id}/fsa/{fsaId}/derp/{derpId}" as the
	// operator named it. Only {id} and {derpId} are used to load the
	// program; {fsaId} is discarded in favor of the value the stored
	// program's own DERControlListLink names (acceptance criterion 1).
	DERProgramHref string

	Type ControlType

	// MaxLimW is required for, and only for, Type == MaxLimW: 0 to 10000
	// hundredths of a percent of setMaxW.
	MaxLimW *uint16

	// PowerFactor is required for, and only for, Type == FixedPFInjectW.
	PowerFactor *PowerFactorValue

	// Start is the requested interval start, Unix seconds. Nil means
	// "start at creationTime" (acceptance criterion 4).
	Start *int64

	// DurationSeconds is the requested interval length, required.
	DurationSeconds uint32
}

// Scope identifies the (EndDevice, FSA, DERProgram) triple a control is
// stored under. FSAID here is the one named by the DERProgram's own
// DERControlListLink, never the FSAID a request happened to carry
// (acceptance criterion 1).
type Scope struct {
	EndDeviceID  string
	FSAID        string
	DERProgramID string
}

// Result is what Issue returns on success.
type Result struct {
	Scope   Scope
	ID      string // the sortable store id (see id.go)
	Href    string // "/edev/{edev}/fsa/{fsa}/derp/{derp}/derc/{id}"
	Control sep2.DERControl

	// Supersedes lists the mRIDs of controls this one fully superseded
	// (same control set, overlapping interval), so a caller can report the
	// effect back to the operator (design section 3, the 201 response).
	Supersedes []string
}

// RefusalCode identifies why Issue or Cancel declined a request. It is a
// closed, stable identifier: a caller maps it to a fixed public message.
// The code never carries request-derived text (acceptance criterion 9).
type RefusalCode string

const (
	RefusalPENNotConfigured   RefusalCode = "pen_not_configured"
	RefusalInvalidProgramHref RefusalCode = "invalid_program_href"
	RefusalProgramNotFound    RefusalCode = "program_not_found"
	RefusalNoControlListLink  RefusalCode = "no_der_control_list_link"
	RefusalStartInPast        RefusalCode = "start_in_past"
	RefusalStartTooFarAhead   RefusalCode = "start_too_far_ahead"
	RefusalDurationOutOfRange RefusalCode = "duration_out_of_range"
	RefusalUnknownType        RefusalCode = "unknown_type"
	RefusalMissingValue       RefusalCode = "missing_value"
	RefusalUnexpectedValue    RefusalCode = "unexpected_value"
	RefusalValueOutOfRange    RefusalCode = "value_out_of_range"
	RefusalControlNotFound    RefusalCode = "control_not_found"
	RefusalAlreadyCancelled   RefusalCode = "already_cancelled"
	RefusalAlreadySuperseded  RefusalCode = "already_superseded"
	RefusalEnded              RefusalCode = "ended"
)

// RefusalError is returned when Issue or Cancel declines. Error() carries
// only the fixed Code, never a request value, so logging or wrapping it
// can never leak operator input (acceptance criterion 9).
type RefusalError struct {
	Code RefusalCode
}

func (e *RefusalError) Error() string {
	return "dercontrol: refused: " + string(e.Code)
}

func refuse(code RefusalCode) error {
	return &RefusalError{Code: code}
}

// programStore is the subset of store.ScopedReader[sep2.DERProgram] the
// issuer needs. Defined at this consumer per the project's Go standard.
type programStore interface {
	Get(ctx context.Context, parentID, id string) (sep2.DERProgram, error)
}

// controlStore is the subset of store.ScopedStore[sep2.DERControl] the
// issuer needs. Delete is used only to undo a Create when the paired
// lifecycle record then fails to store (Issue must never leave a control
// without one).
type controlStore interface {
	Get(ctx context.Context, parentID, id string) (sep2.DERControl, error)
	List(ctx context.Context, parentID string, opts store.ListOptions) (store.ListResult[sep2.DERControl], error)
	Create(ctx context.Context, parentID, id string, resource sep2.DERControl) error
	Delete(ctx context.Context, parentID, id string) error
}

// lifecycleStore is the subset of store.ScopedStore[LifecycleRecord] the
// issuer needs.
type lifecycleStore interface {
	Get(ctx context.Context, parentID, id string) (LifecycleRecord, error)
	Create(ctx context.Context, parentID, id string, resource LifecycleRecord) error
	Update(ctx context.Context, parentID, id string, resource LifecycleRecord) error
}

// Config holds the issuer's tunables. A zero StartLead, MinDuration or
// MaxDuration takes the package default. A configured (non-zero) bound must
// be a positive whole number of seconds, and MinDuration must not exceed
// MaxDuration; NewIssuer rejects a Config that violates either. PEN has no
// default: a nil PEN, or a PEN of 0 (IANA-reserved and therefore treated as
// not configured), makes every Issue call refuse with
// RefusalPENNotConfigured. Wiring these from server configuration is a
// later issue.
type Config struct {
	PEN         *uint32
	StartLead   time.Duration
	MinDuration time.Duration
	MaxDuration time.Duration
}

func (c Config) withDefaults() Config {
	if c.StartLead == 0 {
		c.StartLead = DefaultStartLead
	}
	if c.MinDuration == 0 {
		c.MinDuration = DefaultMinDuration
	}
	if c.MaxDuration == 0 {
		c.MaxDuration = DefaultMaxDuration
	}
	return c
}

// validate reports an error if a bound (already defaulted by withDefaults,
// so a zero here means the field was left unset) is not a positive whole
// number of seconds, or if MinDuration exceeds MaxDuration. A sub-second
// bound is rejected rather than silently truncated: Issue compares against
// int64(d/time.Second), so a 500ms MinDuration would otherwise become 0 and
// accept a zero-length request.
func (c Config) validate() error {
	if err := validatePositiveWholeSeconds("StartLead", c.StartLead); err != nil {
		return err
	}
	if err := validatePositiveWholeSeconds("MinDuration", c.MinDuration); err != nil {
		return err
	}
	if err := validatePositiveWholeSeconds("MaxDuration", c.MaxDuration); err != nil {
		return err
	}
	if c.MinDuration > c.MaxDuration {
		return fmt.Errorf("dercontrol: MinDuration (%s) exceeds MaxDuration (%s)", c.MinDuration, c.MaxDuration)
	}
	return nil
}

func validatePositiveWholeSeconds(name string, d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("dercontrol: %s must be positive, got %s", name, d)
	}
	if d%time.Second != 0 {
		return fmt.Errorf("dercontrol: %s must be a whole number of seconds, got %s", name, d)
	}
	return nil
}
