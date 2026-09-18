package sep2admin

// group is the structural band a Placement's Rank sorts within. Unexported:
// see [ExtensionSlot].
type group uint8

const (
	// groupInvalid is the zero value. A Panel with a zero-value Placement
	// is rejected at Register (ErrZeroPlacement), because group 0 would
	// otherwise sort ahead of the server's own core band, and a graft can
	// always leave Placement unset. Treating the zero value itself as a
	// disallowed condition, rather than trusting every caller to set
	// Placement, is the data-invariants Rule 3 shape applied here.
	groupInvalid group = iota
	groupCore
	groupGraft
)

// Placement fixes where a registered Panel sorts: its band (group) and a
// rank within that band. group is unexported, and ExtensionSlot is the
// only function outside this package that returns a Placement, so a graft
// can construct a Placement only in the graft band. This is enforced by
// the type system, not by convention: a composite literal naming group
// from another package does not compile (proven in
// compile_failure_test.go).
type Placement struct {
	group group

	// Rank orders panels within their own band. It is never compared
	// across bands, because group is compared first in the Registry's
	// sort key.
	Rank int
}

// ExtensionSlot is the only constructor for Placement reachable from
// outside this package. Every Panel placed with it sorts in the graft
// band, after every core-band panel, ordered by (group, Rank, ID).
func ExtensionSlot(rank int) Placement {
	return Placement{group: groupGraft, Rank: rank}
}

// corePlacement is reserved for this package's own future core-panel
// wiring (the server's own topology, EndDevice and FSA panels). It is
// unexported so that only code living inside this package can ever place
// a Panel in the core band: a graft, which by definition imports this
// package from outside, has no way to reach it.
func corePlacement(rank int) Placement {
	return Placement{group: groupCore, Rank: rank}
}

func (p Placement) isZero() bool {
	return p.group == groupInvalid
}
