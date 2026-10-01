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
// MAY change without a major version: nav-ordering internals beyond the
// stated (group, Rank, ID) sort key, theme token names and their
// compiled-in defaults once a Theme carries any, and the reserved-ID list
// as this repository adds routes or tabs of its own.
//
// MAY NOT change without a major version: the Registry method set
// (Register, SetTheme, Freeze) and INV-1, the add-only guarantee those
// three methods exist to hold; the Panel and Placement type signatures;
// ExtensionSlot's signature; the Body and Cell constructors' signatures;
// the Descriptor type and its exported field names; every JSON field name
// on the wire; and the closed sets of section kinds (table,
// definitionList, chart), cell kinds (text, badge, time, link) and badge
// names (neutral, info, ok, warn, error). testdata/descriptor_v2.json is
// the wire shape. A renderer switches exhaustively on each kind. The shell
// is that renderer and ships in the same binary as this encoder, so a kind
// added to both at once stays in version 2, as chart did; removing or
// reshaping a kind is a new CurrentDescriptorVersion.
//
// # What this package does not implement yet
//
// Panel.Assets exists in the signature from day one so the type never has
// to grow a breaking field later, and Register refuses it when set (see
// ErrAssetsNotImplemented): the custom-element mechanism a real
// implementation would need is UNVERIFIED against this repository's
// pinned Svelte version. Theme's parsed fields land in a later issue.
//
// # Known schema gaps
//
// Version 2 adds the shapes version 1 was measured to lack for the
// bridge's six views: several tables per route, prose, empty text,
// captions, badges, links, and time cells with a machine value. No bridge
// view has been rebuilt on it yet. Two gaps remain: ControlFlow's counters
// strip is expressible only as definition entries, and nothing enforces
// that a Row has one Cell per column.
package sep2admin
