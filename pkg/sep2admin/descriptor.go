package sep2admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// bodyKind discriminates which shape a Body carries, if any.
type bodyKind uint8

const (
	bodyKindNone bodyKind = iota
	bodyKindTable
	bodyKindDefinitionList
	bodyKindChart
)

// ErrBodyMarshalledDirectly is returned when a Body is marshalled outside
// its containing Descriptor. json.Marshal would otherwise skip every
// unexported field and silently emit "{}" with no error, the same bytes
// whether the Body carries a table or nothing at all, so a caller could
// not tell a mismarshalled Body from an empty one. Descriptor.MarshalJSON
// never triggers this: it reads Body's shape fields directly and never
// calls json.Marshal on a Body value.
var ErrBodyMarshalledDirectly = errors.New("sep2admin: Body must be marshalled through its containing Descriptor, not directly")

// ErrUnhandledBodyKind is returned by Descriptor.MarshalJSON when a
// Section's Body kind is none of the shapes this package knows how to
// render. Unreachable today, since kind is unexported and only the
// three constructors set it; kept as a named refusal so a future body shape
// cannot fall through a switch silently.
var ErrUnhandledBodyKind = errors.New("sep2admin: Section.Body has an unhandled kind")

// Refusals Descriptor.MarshalJSON returns for content a renderer must not
// receive. Each is checked with errors.Is. They fire at encode, so a View
// that builds one gets an error on the request instead of a payload.
var (
	// ErrSectionWithoutBody is a Section whose Body is the zero Body. A
	// section is a table, a definition list or a chart; one with none has
	// no shape for the renderer to switch on.
	ErrSectionWithoutBody = errors.New("sep2admin: Section has no Body")

	// ErrZeroCell is a zero-value Cell. It is refused rather than encoded
	// as empty text, because a zero Cell is a missing value, and an empty
	// one is TextCell("").
	ErrZeroCell = errors.New("sep2admin: Cell is the zero value (build one with TextCell, BadgeCell, TimeCell or LinkCell)")

	// ErrUnhandledCellKind mirrors ErrUnhandledBodyKind for Cell.
	ErrUnhandledCellKind = errors.New("sep2admin: Cell has an unhandled kind")

	// ErrUnknownBadge is a Badge outside the closed set declared below.
	ErrUnknownBadge = errors.New("sep2admin: Badge is not one of the declared variants")

	// ErrZeroTime is a TimeCell whose instant is the zero time.Time, which
	// is what an unset field produces.
	ErrZeroTime = errors.New("sep2admin: TimeCell has the zero time")

	// ErrUnsafeLink is a LinkCell whose href is not an absolute http or
	// https URL with a host, or a relative reference. javascript:, data:,
	// protocol-relative "//host" and anything carrying a control
	// character, space or backslash are all refused.
	ErrUnsafeLink = errors.New("sep2admin: LinkCell href is not http, https or a relative path")
)

// Body is a Section's shape: a TableBody, a DefinitionListBody or a
// ChartBody. NewTableBody, NewDefinitionListBody and NewChartBody are the
// only functions outside this package that produce a non-zero Body, so a
// Body carrying two shapes stays unconstructible.
//
// The seal is an unexported field, not an unexported interface method: a
// method is promoted through embedding into another package's type, a
// field cannot be selected or promoted from another package at all.
// Placement.group uses the same mechanism (placement.go).
type Body struct {
	kind           bodyKind
	table          TableBody
	definitionList DefinitionListBody
	chart          ChartBody
}

// NewTableBody returns a Body carrying the table shape.
func NewTableBody(b TableBody) Body {
	return Body{kind: bodyKindTable, table: b}
}

// NewDefinitionListBody returns a Body carrying the definition-list shape.
func NewDefinitionListBody(b DefinitionListBody) Body {
	return Body{kind: bodyKindDefinitionList, definitionList: b}
}

// NewChartBody returns a Body carrying the chart shape.
func NewChartBody(b ChartBody) Body {
	return Body{kind: bodyKindChart, chart: b}
}

// MarshalJSON always fails with ErrBodyMarshalledDirectly. A Body is
// rendered only as part of its containing Descriptor. A type that embeds
// Body inherits this method, so marshalling the embedder fails the same
// way.
func (b Body) MarshalJSON() ([]byte, error) {
	return nil, ErrBodyMarshalledDirectly
}

// Value is literal text. This package gives it no method and no sibling
// type that would let a renderer treat it as markup, so a renderer that
// assigns it to text content gets escaping for free.
type Value string

// Badge is a status variant from a closed set. The renderer derives the
// badge's style from the variant's wire name, never from the cell text,
// so the meaning survives a reworded label. Any value outside the
// constants below, the zero value included, is refused at encode.
type Badge uint8

// The declared Badge variants. Their wire names are the badgeNames
// entries, and adding one is a wire change a renderer must learn first.
const (
	BadgeNeutral Badge = iota + 1
	BadgeInfo
	BadgeOK
	BadgeWarn
	BadgeError
)

var badgeNames = map[Badge]string{
	BadgeNeutral: "neutral",
	BadgeInfo:    "info",
	BadgeOK:      "ok",
	BadgeWarn:    "warn",
	BadgeError:   "error",
}

type cellKind uint8

const (
	cellKindNone cellKind = iota
	cellKindText
	cellKindBadge
	cellKindTime
	cellKindLink
)

// Cell is one table cell or one definition entry's value. Its fields are
// unexported, so the four constructors below are the only way to build a
// non-zero Cell outside this package, and each yields exactly one kind.
type Cell struct {
	kind  cellKind
	text  Value
	badge Badge
	at    time.Time
	href  string
}

// TextCell is plain text.
func TextCell(text Value) Cell {
	return Cell{kind: cellKindText, text: text}
}

// BadgeCell is text styled by a variant from the closed Badge set.
func BadgeCell(variant Badge, text Value) Cell {
	return Cell{kind: cellKindBadge, badge: variant, text: text}
}

// TimeCell is an instant with its display text. The instant goes on the
// wire as RFC 3339 in UTC, for a renderer's <time datetime>; display is
// what the operator reads.
func TimeCell(at time.Time, display Value) Cell {
	return Cell{kind: cellKindTime, at: at, text: display}
}

// LinkCell is a hyperlink. href is checked at encode, not here, so a View
// that builds an unsafe one fails its request (ErrUnsafeLink) instead of
// reaching a renderer.
func LinkCell(href string, text Value) Cell {
	return Cell{kind: cellKindLink, href: href, text: text}
}

type wireCell struct {
	Kind     string `json:"kind"`
	Text     Value  `json:"text"`
	Badge    string `json:"badge,omitempty"`
	DateTime string `json:"datetime,omitempty"`
	Href     string `json:"href,omitempty"`
}

// MarshalJSON writes {kind, text} plus the field its kind adds: badge,
// datetime or href. It refuses a zero Cell, an undeclared Badge, a zero
// time and an unsafe href.
func (c Cell) MarshalJSON() ([]byte, error) {
	w := wireCell{Text: c.text}
	switch c.kind {
	case cellKindNone:
		return nil, ErrZeroCell
	case cellKindText:
		w.Kind = "text"
	case cellKindBadge:
		name, ok := badgeNames[c.badge]
		if !ok {
			return nil, fmt.Errorf("%w: %d", ErrUnknownBadge, c.badge)
		}
		w.Kind, w.Badge = "badge", name
	case cellKindTime:
		if c.at.IsZero() {
			return nil, ErrZeroTime
		}
		w.Kind, w.DateTime = "time", c.at.UTC().Format(time.RFC3339Nano)
	case cellKindLink:
		if err := checkHref(c.href); err != nil {
			return nil, err
		}
		w.Kind, w.Href = "link", c.href
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnhandledCellKind, c.kind)
	}
	return json.Marshal(w)
}

// checkHref is the link rule. An href is accepted only when all of these
// hold, and a renderer applying the same rule accepts the same set:
//  1. it is not empty, and holds no byte at or below 0x20 (controls and
//     space), no 0x7F and no backslash;
//  2. if it starts with "http://" or "https://" (scheme in any case), the
//     authority after the "//", up to the first "/", "?" or "#", is not
//     empty and holds no "@";
//  3. otherwise it does not start with "//", and holds no ":" before its
//     first "/", "?" or "#".
//
// Rule 3 refuses "//host", "///host" and longer runs, which a browser
// resolves to another host, and any other scheme. url.Parse then backs
// the rule up.
func checkHref(href string) error {
	if href == "" {
		return fmt.Errorf("%w: empty", ErrUnsafeLink)
	}
	for i := 0; i < len(href); i++ {
		if b := href[i]; b <= ' ' || b == 0x7f || b == '\\' {
			return fmt.Errorf("%w: %q holds byte 0x%02x", ErrUnsafeLink, href, b)
		}
	}
	lower := strings.ToLower(href)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		rest := href[strings.Index(href, "//")+2:]
		authority := rest[:firstOf(rest, "/?#")]
		if authority == "" || strings.Contains(authority, "@") {
			return fmt.Errorf("%w: %q has no usable host", ErrUnsafeLink, href)
		}
	case strings.HasPrefix(href, "//"):
		return fmt.Errorf("%w: %q starts with //", ErrUnsafeLink, href)
	case strings.Contains(href[:firstOf(href, "/?#")], ":"):
		return fmt.Errorf("%w: %q has a scheme other than http or https", ErrUnsafeLink, href)
	}
	u, err := url.Parse(href)
	if err != nil {
		return fmt.Errorf("%w: %q: %w", ErrUnsafeLink, href, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" || u.User != nil {
			return fmt.Errorf("%w: %q", ErrUnsafeLink, href)
		}
	case "":
		if u.Host != "" || u.Opaque != "" {
			return fmt.Errorf("%w: %q", ErrUnsafeLink, href)
		}
	default:
		return fmt.Errorf("%w: %q has scheme %q", ErrUnsafeLink, href, u.Scheme)
	}
	return nil
}

// firstOf is the index of the first byte of s in chars, or len(s).
func firstOf(s, chars string) int {
	if i := strings.IndexAny(s, chars); i >= 0 {
		return i
	}
	return len(s)
}

// Row is one row of a TableBody: one Cell per column, in column order.
// Nothing here enforces len(Row) == len(TableBody.Columns).
type Row []Cell

// TableBody is the table shape: a fixed column set and the rows under it.
type TableBody struct {
	// Columns is the header row, in display order.
	Columns []string

	// Rows is the table body, one Row per record.
	Rows []Row
}

// DefinitionListBody is the definition-list shape: one or more key-value
// groups, each with an optional heading.
type DefinitionListBody struct {
	Groups []DefinitionGroup
}

// DefinitionGroup is one key-value group within a DefinitionListBody.
// Heading is empty when nothing labels the group.
type DefinitionGroup struct {
	Heading string
	Entries []DefinitionEntry
}

// DefinitionEntry is a single key-value pair within a DefinitionGroup.
type DefinitionEntry struct {
	Key   string
	Value Cell
}

// Section is one block of a Descriptor: a heading, explanatory prose, and
// a table, a definition list or a chart. Empty is the text a renderer
// shows in place of the body when it holds no rows, no entries in any
// group, or no points in any series; it is distinct from a zero-row table
// so a panel can say why it is empty.
type Section struct {
	Heading string
	Prose   []string
	Empty   string
	Body    Body
}

// The wire types below write every collection as [], never null, so a
// renderer reads an empty list and a missing one the same single way.

type wireDescriptor struct {
	Version  int           `json:"version"`
	Sections []wireSection `json:"sections"`
}

type wireSection struct {
	Kind    string   `json:"kind"`
	Heading string   `json:"heading"`
	Prose   []string `json:"prose"`
	Empty   string   `json:"empty"`
	Body    any      `json:"body"`
}

type wireTable struct {
	Columns []string `json:"columns"`
	Rows    [][]Cell `json:"rows"`
}

type wireDefinitionList struct {
	Groups []wireGroup `json:"groups"`
}

type wireGroup struct {
	Heading string      `json:"heading"`
	Entries []wireEntry `json:"entries"`
}

type wireEntry struct {
	Key   string `json:"key"`
	Value Cell   `json:"value"`
}

// MarshalJSON writes {version, sections}. Each section carries kind
// ("table", "definitionList" or "chart"), heading, prose, empty and body, and every
// cell carries its own kind. This is the rendering contract a renderer in
// another language reads by field name; testdata/descriptor_v2.json pins
// it byte for byte. Any refusal in a section or a cell fails the whole
// Descriptor, so a renderer never gets a partial payload.
func (d Descriptor) MarshalJSON() ([]byte, error) {
	w := wireDescriptor{Version: d.Version, Sections: make([]wireSection, 0, len(d.Sections))}
	chartPoints := 0
	for i, s := range d.Sections {
		ws := wireSection{Heading: s.Heading, Prose: nonNil(s.Prose), Empty: s.Empty}
		switch s.Body.kind {
		case bodyKindNone:
			return nil, fmt.Errorf("sections[%d]: %w", i, ErrSectionWithoutBody)
		case bodyKindTable:
			ws.Kind, ws.Body = "table", wireTableOf(s.Body.table)
		case bodyKindDefinitionList:
			ws.Kind, ws.Body = "definitionList", wireDefinitionListOf(s.Body.definitionList)
		case bodyKindChart:
			c, err := wireChartOf(s.Body.chart, &chartPoints)
			if err != nil {
				return nil, fmt.Errorf("sections[%d]: %w", i, err)
			}
			ws.Kind, ws.Body = "chart", c
		default:
			return nil, fmt.Errorf("sections[%d]: %w: %d", i, ErrUnhandledBodyKind, s.Body.kind)
		}
		w.Sections = append(w.Sections, ws)
	}
	return json.Marshal(w)
}

func wireTableOf(t TableBody) wireTable {
	rows := make([][]Cell, 0, len(t.Rows))
	for _, r := range t.Rows {
		rows = append(rows, nonNil([]Cell(r)))
	}
	return wireTable{Columns: nonNil(t.Columns), Rows: rows}
}

func wireDefinitionListOf(l DefinitionListBody) wireDefinitionList {
	groups := make([]wireGroup, 0, len(l.Groups))
	for _, g := range l.Groups {
		entries := make([]wireEntry, 0, len(g.Entries))
		for _, e := range g.Entries {
			entries = append(entries, wireEntry(e))
		}
		groups = append(groups, wireGroup{Heading: g.Heading, Entries: entries})
	}
	return wireDefinitionList{Groups: groups}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// RowCount is the number of table rows plus definition entries across
// every section: the rows a renderer puts on the page. Chart points are
// not rows; MaxDescriptorChartPoints bounds them instead.
func (d Descriptor) RowCount() int {
	n := 0
	for _, s := range d.Sections {
		n += len(s.Body.table.Rows)
		for _, g := range s.Body.definitionList.Groups {
			n += len(g.Entries)
		}
	}
	return n
}
