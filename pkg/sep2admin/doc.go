// Package sep2admin is the panel contract for this server's admin UI: the
// types and the add-only registry a caller registers a Panel into, and the
// five boot refusals that keep a graft from ever hiding, replacing, or
// reordering a panel the server itself owns.
//
// It is a sibling of pkg/sep2server, not a part of it. pkg/sep2server's own
// doc.go ties its short exported surface to a stability promise this
// package does not carry: the admin UI is this repository's
// fastest-churning surface, and pkg/sep2server/doc.go names the admin
// dashboard's serving logic, the ACL internals, the operator login
// surface and the test-mutation hooks as staying in internal/, pointing
// at this package and its sibling pkg/adminui/web for the UI's own,
// weaker, promises instead. Placing the panel contract under
// pkg/sep2server's promise would either freeze the UI or devalue the
// promise; there is no third option.
//
// # Stability
//
// This package's promise is weaker than pkg/sep2server's, stated in terms
// rather than as the word "weaker":
//
// MAY change without a major version: TableBody's and DefinitionListBody's
// own fields as rendering needs grow (added fields only: a renderer reads
// known keys and ignores the rest), nav-ordering internals beyond the
// stated (group, Rank, ID) sort key, theme token names and their
// compiled-in defaults once a Theme carries any, and the reserved-ID list
// as this repository adds routes of its own.
//
// MAY NOT change without a major version: the Registry method set
// (Register, SetTheme, Freeze) and INV-1, the add-only guarantee those
// three methods exist to hold; the Panel and Placement type signatures;
// ExtensionSlot's signature, since a graft's call sites depend on it
// directly; NewTableBody's and NewDefinitionListBody's signatures, for
// the same reason: they are the only functions outside this package that
// produce a non-zero Body, so a graft's call sites depend on them exactly
// as they depend on ExtensionSlot's; the Descriptor type and its exported
// field names (Version, Body); every JSON field name on the wire (version,
// kind, body, columns, rows, groups, heading, entries, key, value); and
// the two kind values a Descriptor's body can ever carry, table and
// definitionList. A third
// Body shape is a major-version change even though NewTableBody and
// NewDefinitionListBody would stay additive from a Go caller's side: a
// renderer that switches exhaustively on kind, which is the whole point
// of naming it explicitly rather than inferring the shape, must never
// receive a kind value it was not built to handle.
//
// # What this package does not implement yet
//
// Panel.Assets exists in the signature from day one so the type never has
// to grow a breaking field later, and Register refuses it when set (see
// ErrAssetsNotImplemented): the custom-element mechanism a real
// implementation would need is UNVERIFIED against this repository's
// pinned Svelte version. Theme's parsed fields, the shell's actual
// renderer for TableBody and DefinitionListBody, and route mounting land
// in later issues; this package fixes their contract, not their
// behavior.
//
// # Known schema gaps (#368 criterion 2 REFUSAL)
//
// Measured against the bridge's 6 route components at
// gridappsd-ieee-2030_5-go's b5f633f, recorded rather than worked around;
// criterion 2 asks for this server's own inexpressible panels by name,
// and closing #368 means resolving that scope gap, not this list:
//
//   - Multiple tables on one route. ServedResources (2 tables) and
//     ConnectedClients (3) each need more than one table where a
//     Descriptor carries exactly one Body. That is 2 of the 4
//     table-bearing components measured, not a remote edge case, so a
//     Tables []TableBody-shaped version bump is near-certain.
//   - Prose. All 6 of 6 components open with an explanatory paragraph
//     (32 <p> elements measured in total). Neither TableBody nor
//     DefinitionListBody has anywhere to put one, so even a
//     single-table or single-group panel is not fully expressible today.
//   - A link with a dynamic href. Health.svelte renders one; Value has
//     no link shape. Health is this package's own stated example of an
//     "expressible" single-group definition list, and that claim holds
//     only for its key-value pairs, not for this link.
//   - ControlFlow's counters strip (Applied, Skipped) and its idle
//     message. Its two connection-topic and last-applied-delta groups
//     are expressible as two DefinitionGroups; the counters and the
//     idle message are not definition entries and are not covered by
//     that shape.
//   - Per-section empty-state text ("No registry entries yet."),
//     distinct from a zero-row table.
//   - A caption above a table. DefinitionGroup has Heading;
//     TableBody has no equivalent field.
//   - Status badges. ConnectedClients.svelte (6) and Registry.svelte (2)
//     render 8 `<span class="badge ...">` cells whose class carries the
//     meaning (connected/never-connected, accepted/rejected,
//     placeholder/certificate). A Value is plain text, so a renderer
//     reading {"value":"rejected"} can only recover the variant by
//     string-matching the text.
//   - A cell with a machine value distinct from its display text.
//     ConnectedClients.svelte:154 and :247 each render
//     <time datetime={v}>{humanized}</time>: an ISO timestamp for
//     machines and a humanized string for the eye, in one cell. Value is
//     one string.
package sep2admin
