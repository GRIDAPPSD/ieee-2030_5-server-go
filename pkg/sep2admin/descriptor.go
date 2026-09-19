package sep2admin

import "encoding/json"

// bodyKind discriminates which shape a Body carries, if any.
type bodyKind uint8

const (
	bodyKindNone bodyKind = iota
	bodyKindTable
	bodyKindDefinitionList
)

// Body is a Descriptor's rendering payload: a TableBody, a
// DefinitionListBody, or the zero Body for none. NewTableBody and
// NewDefinitionListBody are the only functions outside this package
// that produce a non-zero Body, so "both a table and a definition list"
// stays unconstructible: there is no second field to set.
//
// The seal is an unexported field (kind), not an unexported interface
// method, and that distinction is the whole fix: an unexported
// interface method is promoted through embedding, so a type in another
// package that embeds an already-implementing exported type (this
// package's own TableBody, under the previous design) inherits the
// promoted method and satisfies the interface without declaring
// anything itself, carrying whatever extra fields it likes. An
// unexported FIELD cannot be selected, or promoted through embedding,
// from another package at any depth; that is the same mechanism
// Placement.group already uses to keep a graft out of the core band
// (placement.go). Embedding cannot smuggle a field into the wire
// payload either: NewTableBody takes a TableBody by value, and a type
// that embeds TableBody to add a field is not itself a TableBody, so
// passing it does not compile.
type Body struct {
	kind           bodyKind
	table          TableBody
	definitionList DefinitionListBody
}

// NewTableBody returns a Body carrying the table shape.
func NewTableBody(b TableBody) Body {
	return Body{kind: bodyKindTable, table: b}
}

// NewDefinitionListBody returns a Body carrying the definition-list shape.
func NewDefinitionListBody(b DefinitionListBody) Body {
	return Body{kind: bodyKindDefinitionList, definitionList: b}
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
// one TableBody. That is 2 of the 4 table-bearing components measured,
// not a remote edge case: see doc.go's "Known schema gaps", which this
// gap is reported to rather than solved by this type.
type TableBody struct {
	// Columns is the header row, in display order.
	Columns []string `json:"columns"`

	// Rows is the table body, one Row per record.
	Rows []Row `json:"rows"`
}

// DefinitionListBody is the definition-list shape: one or more key-value
// groups. A panel whose list is a single flat set (the bridge's Health
// panel) sends one DefinitionGroup with an empty Heading. A panel whose
// <dl> is naturally sectioned (the bridge's ControlFlow panel renders
// two: connection topics, and the last applied control delta) sends one
// DefinitionGroup per section. This is why "groups", plural, is the
// shape: criterion 4 names a definition list as "key-value groups", and
// the bridge's own measured ControlFlow count (2 definition lists on one
// route) is exactly one panel needing two. This covers ControlFlow's
// headings and key-value entries only: its counters strip and its idle
// message are neither, and are not expressible here either; see doc.go's
// "Known schema gaps".
type DefinitionListBody struct {
	Groups []DefinitionGroup `json:"groups"`
}

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
// are both omitted when a Descriptor carries no Body. Body is typed any
// rather than Body (the sealed struct) because the wire value is
// TableBody's or DefinitionListBody's own fields, not Body's unexported
// discriminant.
type wireDescriptor struct {
	Version int    `json:"version"`
	Kind    string `json:"kind,omitempty"`
	Body    any    `json:"body,omitempty"`
}

// MarshalJSON writes Descriptor as {version, kind, body}: version always,
// and kind plus body together, present only when Body carries a shape.
// body is the shape's own fields, marshalled by the standard library's
// normal struct encoding; there is no custom encoding on TableBody or
// DefinitionListBody themselves, since Body's seal no longer depends on
// either of them having a method.
//
// This is the rendering contract a renderer in another language reads by
// field name. Nothing in this package unmarshals a Descriptor back from
// JSON: Body's sealed fields mean a naive decode into this shape would
// produce a map, not a TableBody or a DefinitionListBody, and nothing in
// this repository reads a Descriptor from the wire yet. Decoding is out
// of scope until a consumer needs it.
func (d Descriptor) MarshalJSON() ([]byte, error) {
	w := wireDescriptor{Version: d.Version}
	switch d.Body.kind {
	case bodyKindTable:
		w.Kind = "table"
		w.Body = d.Body.table
	case bodyKindDefinitionList:
		w.Kind = "definitionList"
		w.Body = d.Body.definitionList
	}
	return json.Marshal(w)
}
