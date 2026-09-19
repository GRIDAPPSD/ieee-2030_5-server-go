package sep2admin

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

// Sentinel errors for the five named boot refusals, plus the two
// standalone refusals on Assets and DescriptorVersion. Every one is meant
// to be checked with errors.Is, never by comparing strings, so a caller
// can tell which refusal fired.
var (
	// ErrZeroPlacement is refusal 1 of 5: a Panel with a zero-value
	// Placement. Group 0 would sort ahead of the server's own band, and a
	// graft can always leave Placement unset, so this is checked
	// explicitly rather than left to the zero value's accidental
	// ordering.
	ErrZeroPlacement = errors.New("sep2admin: panel has a zero-value Placement (build one with ExtensionSlot)")

	// ErrDuplicateID is refusal 2 of 5. The first registration of an ID
	// always survives: overwrite would be replacement wearing
	// registration's clothes, and INV-1 forbids replacement.
	ErrDuplicateID = errors.New("sep2admin: panel ID already registered")

	// ErrInvalidID is refusal 3 of 5: an ID that fails the strict slug
	// pattern, or one that collides with a name this repository reserves
	// for its own routes. A panel's ID reaches the URL space, so an
	// unvalidated traversal segment or reserved name would shadow a
	// server-owned route.
	ErrInvalidID = errors.New("sep2admin: invalid panel ID")

	// ErrRegistryFrozen is refusal 4 of 5: any Register or SetTheme after
	// Freeze, and any second call to Freeze itself. Freeze runs once,
	// before serving starts.
	ErrRegistryFrozen = errors.New("sep2admin: registry is frozen")

	// ErrCorePanelsMissing is refusal 5 of 5: Freeze with no core-band
	// panel registered. A boot missing the server's own core panels fails
	// rather than serving a shell without them.
	ErrCorePanelsMissing = errors.New("sep2admin: no core panel registered before Freeze")

	// ErrNilView is returned by Register when a Panel's View is nil. A
	// nil View registers cleanly today and then reports
	// "ErrViewPanicked: nil pointer dereference" on its first request,
	// which is exactly the rendering surprise CurrentDescriptorVersion's
	// own boot-failure rule forbids: refusing it here makes a missing
	// View a boot failure instead of a first-request one.
	ErrNilView = errors.New("sep2admin: panel has a nil View")

	// ErrAssetsNotImplemented is returned by Register when a Panel's
	// Assets field is non-nil. Nothing in this package mounts an Assets
	// filesystem yet; a graft that sets it is refused rather than
	// half-mounted.
	ErrAssetsNotImplemented = errors.New("sep2admin: Panel.Assets is not implemented")

	// ErrUnsupportedDescriptorVersion is returned by Register when a
	// Panel's DescriptorVersion does not equal CurrentDescriptorVersion.
	ErrUnsupportedDescriptorVersion = errors.New("sep2admin: unsupported Panel.DescriptorVersion")
)

// ErrDisabled is the preserved sentinel from the bridge's
// internal/adminui.ErrDisabled: "the admin UI is not configured" is not a
// failure. A caller should errors.Is this and skip starting a runner,
// never treat it as a startup error. It never matches, and is never
// matched by, any of the refusal errors above.
var ErrDisabled = errors.New("sep2admin: admin UI not configured, this is not a failure")

// idPattern is the strict slug an ID must match: lowercase, starting with
// a letter, ASCII letters/digits/hyphens only, capped at 63 characters.
// Neither "." nor "/" is in the class, so no traversal segment can ever
// match.
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// reservedIDs are names this repository's own routes already use, or has
// committed to using. A panel ID colliding with one of these would shadow
// a server-owned route rather than merely occupying URL space, so
// Register refuses it the same way it refuses a malformed ID. This set
// includes the admin UI's core tab slugs and its top-level path segments,
// so the two lists cannot drift apart silently.
var reservedIDs = map[string]struct{}{
	// Core tab slugs.
	"overview":     {},
	"devices":      {},
	"fsas":         {},
	"control":      {},
	"certificates": {},
	// Top-level path segments.
	"ui":        {},
	"api":       {},
	"auth":      {},
	"login":     {},
	"dashboard": {},
}

// Registry is the add-only contract a Panel is registered through. Its
// only verb is Register, plus SetTheme (the skinning seam) and Freeze.
// There is no Remove, no Replace, no Hide, and no mutable order field
// anywhere on this interface: INV-1 holds because the type does not offer
// the alternative, so a later addition of one of those methods breaks
// TestRegistryExportedMethodSet rather than passing review.
type Registry interface {
	// Register adds a Panel. It is the registry's only mutating verb
	// besides SetTheme; there is no way to remove, replace, or reorder a
	// panel once registered.
	Register(Panel) error

	// SetTheme sets the graft-supplied skin. This package carries only
	// the seam and its Freeze/frozen refusal semantics; a later issue
	// defines Theme's parsed fields.
	SetTheme(Theme) error

	// Freeze seals the registry: no further Register or SetTheme call
	// succeeds, including a second Freeze. It refuses with
	// ErrCorePanelsMissing if no core-band panel was registered, and
	// otherwise returns every registered Panel sorted by (group, Rank,
	// ID), with the core band first by construction.
	Freeze() ([]Panel, error)
}

// registeredPanel wraps a Panel with its registration sequence, recorded
// for debugging only. seq is never read by the sort in Freeze: composing-
// main init order and map iteration are not deterministic across restarts,
// so treating registration order as a tie-break would shuffle the nav
// between runs. Freeze's own reordering test is what proves seq is
// excluded, since seq itself is never returned to a caller.
type registeredPanel struct {
	Panel
	seq int
}

type registry struct {
	mu     sync.Mutex
	panels map[string]registeredPanel
	next   int
	theme  *Theme
	frozen bool
}

// NewRegistry returns an empty, unfrozen Registry.
func NewRegistry() Registry {
	return &registry{panels: make(map[string]registeredPanel)}
}

func (r *registry) Register(p Panel) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.frozen {
		return ErrRegistryFrozen
	}
	if p.Placement.isZero() {
		return ErrZeroPlacement
	}
	if err := validateID(p.ID); err != nil {
		return err
	}
	if _, exists := r.panels[p.ID]; exists {
		return fmt.Errorf("%w: %q", ErrDuplicateID, p.ID)
	}
	if p.View == nil {
		return ErrNilView
	}
	if p.Assets != nil {
		return ErrAssetsNotImplemented
	}
	if p.DescriptorVersion != CurrentDescriptorVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedDescriptorVersion, p.DescriptorVersion, CurrentDescriptorVersion)
	}

	r.panels[p.ID] = registeredPanel{Panel: p, seq: r.next}
	r.next++
	return nil
}

func (r *registry) SetTheme(t Theme) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.frozen {
		return ErrRegistryFrozen
	}
	r.theme = &t
	return nil
}

func (r *registry) Freeze() ([]Panel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.frozen {
		return nil, ErrRegistryFrozen
	}

	sorted := make([]registeredPanel, 0, len(r.panels))
	haveCore := false
	for _, p := range r.panels {
		if p.Placement.group == groupCore {
			haveCore = true
		}
		sorted = append(sorted, p)
	}
	if !haveCore {
		return nil, ErrCorePanelsMissing
	}

	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Placement.group != b.Placement.group {
			return a.Placement.group < b.Placement.group
		}
		if a.Placement.Rank != b.Placement.Rank {
			return a.Placement.Rank < b.Placement.Rank
		}
		return a.ID < b.ID
	})

	// The type-level ordering guarantee (groupCore < groupGraft) already
	// makes this unreachable; asserted anyway because it is its own named
	// refusal, not only a consequence of the haveCore check above.
	if sorted[0].Placement.group != groupCore {
		return nil, ErrCorePanelsMissing
	}

	r.frozen = true
	out := make([]Panel, len(sorted))
	for i, p := range sorted {
		out[i] = p.Panel
	}
	return out, nil
}

func validateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("%w: %q does not match the slug pattern", ErrInvalidID, id)
	}
	if _, reserved := reservedIDs[id]; reserved {
		return fmt.Errorf("%w: %q is a reserved name", ErrInvalidID, id)
	}
	return nil
}
