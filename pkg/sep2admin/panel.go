package sep2admin

import (
	"context"
	"io/fs"
)

// CurrentDescriptorVersion is the schema version Register requires a
// Panel's DescriptorVersion to equal. A mismatch is refused at Register
// (ErrUnsupportedDescriptorVersion): a version drift is a boot failure,
// not a rendering surprise discovered on the first request.
const CurrentDescriptorVersion = 1

// Descriptor is the versioned payload a Panel's View produces. Later
// issues define the renderer-facing body (a table shape and a
// definition-list shape); this package fixes only the version field the
// boot-time check in Register depends on.
type Descriptor struct {
	// Version is the schema version this Descriptor was produced against.
	// A renderer is expected to refuse anything other than
	// CurrentDescriptorVersion rather than guess at an unknown shape.
	Version int
}

// ViewFunc renders a Panel's content as a Descriptor. The server supplies
// its own implementations for its core panels; a graft supplies its own.
type ViewFunc func(ctx context.Context) (Descriptor, error)

// Panel is the unit a caller registers into a Registry: what to render
// (View), where it sorts (Placement), and its own identity.
type Panel struct {
	// ID is the panel's slug. It reaches the URL space once route
	// mounting lands, so Register validates it against a strict pattern
	// and a reserved-name list rather than trusting it: a traversal
	// segment or a literal reserved name in an unvalidated ID would
	// shadow a server-owned route.
	ID string

	// Label is the panel's nav text.
	Label string

	// Placement fixes where the panel sorts. Build it with
	// [ExtensionSlot].
	Placement Placement

	// DescriptorVersion is the schema version this Panel's View produces.
	// Register refuses a Panel whose DescriptorVersion does not equal
	// CurrentDescriptorVersion.
	DescriptorVersion int

	// View renders the panel.
	View ViewFunc

	// Assets is the escape hatch for a panel that cannot be expressed as
	// a descriptor: a filesystem the shell would serve under the panel's
	// own namespace. It is nil in every panel this repository ships and
	// UNIMPLEMENTED here: Register refuses a non-nil Assets with
	// ErrAssetsNotImplemented rather than half-mounting it. The
	// custom-element mechanism a real implementation would need is
	// UNVERIFIED against this repository's pinned Svelte version.
	Assets fs.FS
}
