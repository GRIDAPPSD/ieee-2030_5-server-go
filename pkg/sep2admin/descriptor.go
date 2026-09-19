package sep2admin

import "encoding/json"

// Body is a Descriptor's rendering payload: a TableBody, a
// DefinitionListBody, or nil for a Descriptor with no body. It is sealed
// to this package's two shapes by an unexported method, the same pattern
// Placement uses to keep its group unreachable from outside the package:
// nothing outside sep2admin can implement Body, so the only types that
// can ever reach Descriptor.Body are the two defined here.
//
// Descriptor holds Body in a single field rather than one optional field
// per shape, so "both a table and a definition list" is not a state a
// caller can construct: there is no second field to set. That is the
// type-system refusal criterion 2 (#368) asks for; there is no
// constructor and no runtime check, because none is needed to hold the
// invariant.
type Body interface {
	// bodyKind names this Body's shape for the wire discriminator
	// Descriptor.MarshalJSON writes. Unexported: it is the method that
	// seals Body to this package.
	bodyKind() string
}

// Value is a single piece of rendered content: one TableBody cell or one
// DefinitionEntry's value. Its only underlying type is string, and this
// package gives it no method and no sibling type that would let a
// renderer treat it as anything other than literal text. A renderer that
// assigns a Value to an element's text content (Svelte's default
// {expression} interpolation, or an equivalent elsewhere) gets escaping
// for free, because Value offers no more-privileged sink to assign it to
// instead; getting that wrong takes a renderer going out of its way, for
// example by writing a Value into innerHTML rather than textContent.
type Value string

// Row is one row of a TableBody: one Value per column, in column order.
// Nothing in this package enforces len(Row) == len(TableBody.Columns) at
// construction, since Go has no fixed-width slice type over a variable
// column count; a row of the wrong length is a bug for a renderer or a
// later validation pass to catch, not one this type can refuse.
type Row []Value

// TableBody is the table shape: a fixed column set and the rows under
// it. It carries exactly one table. The bridge's own ServedResources and
// ConnectedClients panels each render more than one independent table on
// a single route (2 and 3 respectively, verified against the bridge's
// origin/main); a panel needing several tables is not expressible with
// one TableBody, and that gap is reported to #368's criterion 2 list
// rather than solved by this type.
type TableBody struct {
	// Columns is the header row, in display order.
	Columns []string `json:"columns"`

	// Rows is the table body, one Row per record.
	Rows []Row `json:"rows"`
}

func (TableBody) bodyKind() string { return "table" }

// DefinitionListBody is the definition-list shape: one or more key-value
// groups. A panel whose list is a single flat set (the bridge's Health
// panel) sends one DefinitionGroup with an empty Heading. A panel whose
// <dl> is naturally sectioned (the bridge's ControlFlow panel renders
// two: connection topics, and the last applied control delta) sends one
// DefinitionGroup per section. This is why "groups", plural, is the
// shape: criterion 4 names a definition list as "key-value groups", and
// the bridge's own measured ControlFlow count (2 definition lists on one
// route) is exactly one panel needing two.
type DefinitionListBody struct {
	Groups []DefinitionGroup `json:"groups"`
}

func (DefinitionListBody) bodyKind() string { return "definitionList" }

// DefinitionGroup is one key-value group within a DefinitionListBody.
// Heading is empty when a body has exactly one group and nothing labels
// it.
type DefinitionGroup struct {
	Heading string            `json:"heading,omitempty"`
	Entries []DefinitionEntry `json:"entries"`
}

// DefinitionEntry is a single key-value pair within a DefinitionGroup.
type DefinitionEntry struct {
	Key   string `json:"key"`
	Value Value  `json:"value"`
}

// wireDescriptor is the JSON shape Descriptor.MarshalJSON produces.
// Kind is an explicit discriminator naming which Body shape follows,
// so a renderer reads which shape it got by name rather than by
// inferring it from which fields happen to be present. Kind and Body
// are both omitted when a Descriptor carries no Body.
type wireDescriptor struct {
	Version int    `json:"version"`
	Kind    string `json:"kind,omitempty"`
	Body    Body   `json:"body,omitempty"`
}

// MarshalJSON writes Descriptor as {version, kind, body}: version always,
// and kind plus body together, present only when Body is non-nil. Kind is
// TableBody's or DefinitionListBody's bodyKind(); body is the shape's own
// fields, marshalled by the standard library's normal struct encoding.
//
// This is the rendering contract a renderer in another language reads by
// field name. Nothing in this package unmarshals a Descriptor back from
// JSON: Body's sealed interface means a naive decode into this shape
// would produce a map, not a TableBody or a DefinitionListBody, and
// nothing in this repository reads a Descriptor from the wire yet.
// Decoding is out of scope until a consumer needs it.
func (d Descriptor) MarshalJSON() ([]byte, error) {
	w := wireDescriptor{Version: d.Version, Body: d.Body}
	if d.Body != nil {
		w.Kind = d.Body.bodyKind()
	}
	return json.Marshal(w)
}
