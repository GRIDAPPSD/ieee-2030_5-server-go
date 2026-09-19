// Package sep2admin is the panel contract for this server's admin UI: the
// types and the add-only registry a caller registers a Panel into, and the
// five boot refusals that keep a graft from ever hiding, replacing, or
// reordering a panel the server itself owns.
//
// It is a sibling of pkg/sep2server, not a part of it. pkg/sep2server's own
// doc.go ties its short exported surface to a stability promise this
// package does not carry: the admin UI is this repository's
// fastest-churning surface, and pkg/sep2server/doc.go names the admin
// dashboard, the ACL internals, the operator login surface and the
// test-mutation hooks as staying in internal/ specifically so that promise
// stays affordable. Placing the panel contract under that promise would
// either freeze the UI or devalue the promise; there is no third option.
//
// # Stability
//
// This package's promise is weaker than pkg/sep2server's, stated in terms
// rather than as the word "weaker":
//
// MAY change without a major version: TableBody's and DefinitionListBody's
// own fields as rendering needs grow, nav-ordering internals beyond the
// stated (group, Rank, ID) sort key, theme token names and their
// compiled-in defaults once a Theme carries any, and the reserved-ID list
// as this repository adds routes of its own.
//
// MAY NOT change without a major version: the Registry method set
// (Register, SetTheme, Freeze) and INV-1, the add-only guarantee those
// three methods exist to hold; the Panel and Placement type signatures;
// and ExtensionSlot's signature, since a graft's call sites depend on it
// directly.
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
package sep2admin
