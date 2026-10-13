package sep2admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wireFixture is the v2 wire shape a renderer in another language is
// written against. TestDescriptorV2MatchesWireFixture holds the encoder to
// it byte for byte, so a change to either side is a visible diff.
var wireFixture = filepath.Join("testdata", "descriptor_v2.json")

// chartAt is 2025-10-01T18:30:00Z, 1759343400000 on the wire.
var chartAt = time.UnixMilli(1759343400000)

// hostileSeriesName is the series name the renderer must show as text.
const hostileSeriesName = "<img src=x onerror=alert(1)>"

// fixtureDescriptor exercises every section kind and every cell kind, with
// two tables and a chart beside the second, so the fixture shows each wire
// field at least once.
func fixtureDescriptor() Descriptor {
	at := time.Date(2026, 10, 1, 12, 30, 0, 0, time.FixedZone("x", -6*3600))
	return Descriptor{
		Version: CurrentDescriptorVersion,
		Sections: []Section{
			{
				Heading: "Registry",
				Prose:   []string{"Devices the bridge has registered."},
				Empty:   "No registry entries yet.",
				Body: NewTableBody(TableBody{
					Columns: []string{"Name", "State", "Last seen", "Docs"},
					Rows: []Row{
						{TextCell("pv-1"), BadgeCell(BadgeOK, "accepted"), TimeCell(at, "5 minutes ago"), LinkCell("/ui/devices", "devices")},
						{TextCell("pv-2"), BadgeCell(BadgeError, "rejected"), TimeCell(at, "5 minutes ago"), LinkCell("https://example.org/a?b=c", "spec")},
					},
				}),
			},
			{
				Heading: "Clients",
				Empty:   "No clients connected.",
				Body:    NewTableBody(TableBody{Columns: []string{"LFDI"}}),
			},
			{
				Heading: "State of charge",
				Prose:   []string{"Battery state of charge, one sample a minute."},
				Empty:   "No samples yet.",
				Body: NewChartBody(ChartBody{
					Unit: "%",
					Series: []ChartSeries{
						{Name: "bat-1", Points: []ChartPoint{{At: chartAt, Value: 65.5}, {At: chartAt.Add(time.Minute), Value: 66}}},
						{Name: hostileSeriesName, Points: []ChartPoint{{At: chartAt, Value: -0.25}}},
						{Name: "bat-3"},
					},
				}),
			},
			{
				Body: NewDefinitionListBody(DefinitionListBody{
					Groups: []DefinitionGroup{
						{Heading: "Connection", Entries: []DefinitionEntry{
							{Key: "Status", Value: BadgeCell(BadgeWarn, "degraded")},
							{Key: "Topic", Value: TextCell("goss/gridappsd/simulation/output")},
						}},
						{Entries: []DefinitionEntry{
							{Key: "Badges", Value: BadgeCell(BadgeNeutral, "n")},
							{Key: "Info", Value: BadgeCell(BadgeInfo, "i")},
						}},
					},
				}),
			},
		},
	}
}

func TestDescriptorV2MatchesWireFixture(t *testing.T) {
	got, err := json.Marshal(fixtureDescriptor())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	raw, err := os.ReadFile(wireFixture)
	if err != nil {
		t.Fatalf("read %s: %v", wireFixture, err)
	}
	var want bytes.Buffer
	if err := json.Compact(&want, raw); err != nil {
		t.Fatalf("compact %s: %v", wireFixture, err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("wire bytes differ from %s\n got: %s\nwant: %s", wireFixture, got, want.Bytes())
	}
}

// TestDescriptorV2FieldValues reads the encoded bytes back by field name,
// the way a renderer does, for the two-tables-and-a-badge criterion.
func TestDescriptorV2FieldValues(t *testing.T) {
	b, err := json.Marshal(fixtureDescriptor())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got struct {
		Version  int `json:"version"`
		Sections []struct {
			Kind    string   `json:"kind"`
			Heading string   `json:"heading"`
			Prose   []string `json:"prose"`
			Empty   string   `json:"empty"`
			Body    struct {
				Columns []string `json:"columns"`
				Rows    [][]struct {
					Kind     string `json:"kind"`
					Text     string `json:"text"`
					Badge    string `json:"badge"`
					DateTime string `json:"datetime"`
					Href     string `json:"href"`
				} `json:"rows"`
			} `json:"body"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, b)
	}
	if got.Version != 2 {
		t.Errorf("version = %d, want the literal 2", got.Version)
	}
	if len(got.Sections) != 4 || got.Sections[0].Kind != "table" || got.Sections[1].Kind != "table" || got.Sections[2].Kind != "chart" || got.Sections[3].Kind != "definitionList" {
		t.Fatalf("sections = %+v, want table, table, chart, definitionList", got.Sections)
	}
	s := got.Sections[0]
	if s.Heading != "Registry" || s.Empty != "No registry entries yet." || len(s.Prose) != 1 || s.Prose[0] != "Devices the bridge has registered." {
		t.Errorf("section 0 heading/prose/empty = %q %q %q", s.Heading, s.Prose, s.Empty)
	}
	if len(s.Body.Rows) != 2 || len(s.Body.Rows[0]) != 4 {
		t.Fatalf("section 0 rows = %+v, want 2 rows of 4", s.Body.Rows)
	}
	badge := s.Body.Rows[0][1]
	if badge.Kind != "badge" || badge.Badge != "ok" || badge.Text != "accepted" {
		t.Errorf("badge cell = %+v, want kind badge, badge ok, text accepted", badge)
	}
	tm := s.Body.Rows[0][2]
	if tm.Kind != "time" || tm.DateTime != "2026-10-01T18:30:00Z" || tm.Text != "5 minutes ago" {
		t.Errorf("time cell = %+v, want datetime 2026-10-01T18:30:00Z in UTC", tm)
	}
	link := s.Body.Rows[1][3]
	if link.Kind != "link" || link.Href != "https://example.org/a?b=c" || link.Text != "spec" {
		t.Errorf("link cell = %+v", link)
	}
	if text := s.Body.Rows[0][0]; text.Kind != "text" || text.Text != "pv-1" || text.Badge != "" || text.Href != "" || text.DateTime != "" {
		t.Errorf("text cell = %+v, want only kind and text", text)
	}
}

// TestDescriptorV2EmptyCollectionsAreArrays inspects bytes: an empty list
// is [] on the wire, never null, and never an absent key.
func TestDescriptorV2EmptyCollectionsAreArrays(t *testing.T) {
	cases := []struct {
		name string
		d    Descriptor
		want string
	}{
		{"no sections", Descriptor{Version: CurrentDescriptorVersion}, `{"version":2,"sections":[]}`},
		{"empty table", Descriptor{Version: CurrentDescriptorVersion, Sections: []Section{{Body: NewTableBody(TableBody{})}}},
			`{"version":2,"sections":[{"kind":"table","heading":"","prose":[],"empty":"","body":{"columns":[],"rows":[]}}]}`},
		{"empty row", Descriptor{Version: CurrentDescriptorVersion, Sections: []Section{{Body: NewTableBody(TableBody{Rows: []Row{nil}})}}},
			`{"version":2,"sections":[{"kind":"table","heading":"","prose":[],"empty":"","body":{"columns":[],"rows":[[]]}}]}`},
		{"empty list", Descriptor{Version: CurrentDescriptorVersion, Sections: []Section{{Body: NewDefinitionListBody(DefinitionListBody{Groups: []DefinitionGroup{{}}})}}},
			`{"version":2,"sections":[{"kind":"definitionList","heading":"","prose":[],"empty":"","body":{"groups":[{"heading":"","entries":[]}]}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.d)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if string(b) != tc.want {
				t.Errorf("got  %s\nwant %s", b, tc.want)
			}
		})
	}
}

func oneCell(c Cell) Descriptor {
	return Descriptor{Version: CurrentDescriptorVersion, Sections: []Section{{
		Body: NewTableBody(TableBody{Columns: []string{"c"}, Rows: []Row{{c}}}),
	}}}
}

func TestLinkCellHrefIsCheckedAtEncode(t *testing.T) {
	refused := []string{
		"javascript:x",
		"JavaScript:alert(1)",
		" javascript:x",
		"java\tscript:x",
		"data:text/html,<b>x</b>",
		"vbscript:x",
		"//evil.example/x",
		"/\\evil.example",
		"http:no-host",
		"https://user@evil.example/",
		"",
		"ftp://example.org/",
		"///evil.example",
		"////evil.example",
		"///evil.example/a?b",
		"http:///evil.example",
		"https:evil.example",
		"HTTP:/evil.example",
	}
	for _, href := range refused {
		_, err := json.Marshal(oneCell(LinkCell(href, "x")))
		if !errors.Is(err, ErrUnsafeLink) {
			t.Errorf("href %q: err = %v, want ErrUnsafeLink", href, err)
		}
	}
	allowed := []string{"http://example.org", "HTTPS://example.org/a#b", "/ui/fsas", "relative/path", "../up", "?q=1", "#frag"}
	for _, href := range allowed {
		b, err := json.Marshal(oneCell(LinkCell(href, "x")))
		if err != nil {
			t.Errorf("href %q: %v, want accepted", href, err)
			continue
		}
		want, _ := json.Marshal(href)
		if !strings.Contains(string(b), `"href":`+string(want)) {
			t.Errorf("href %q: encoded %s does not carry it unchanged", href, b)
		}
	}
}

func TestCellRefusalsAtEncode(t *testing.T) {
	cases := []struct {
		name string
		c    Cell
		want error
	}{
		{"zero cell", Cell{}, ErrZeroCell},
		{"zero badge", BadgeCell(0, "x"), ErrUnknownBadge},
		{"undeclared badge", BadgeCell(BadgeError+1, "x"), ErrUnknownBadge},
		{"zero time", TimeCell(time.Time{}, "never"), ErrZeroTime},
		{"unhandled kind", Cell{kind: cellKind(99)}, ErrUnhandledCellKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := json.Marshal(oneCell(tc.c))
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEveryDeclaredBadgeHasAWireName(t *testing.T) {
	want := map[Badge]string{BadgeNeutral: "neutral", BadgeInfo: "info", BadgeOK: "ok", BadgeWarn: "warn", BadgeError: "error"}
	if len(badgeNames) != len(want) {
		t.Fatalf("badgeNames has %d entries, want %d", len(badgeNames), len(want))
	}
	for b, name := range want {
		if badgeNames[b] != name {
			t.Errorf("badge %d wire name = %q, want %q", b, badgeNames[b], name)
		}
	}
}

func TestSectionRefusalsAtEncode(t *testing.T) {
	cases := []struct {
		name string
		s    Section
		want error
	}{
		{"no body", Section{Heading: "h"}, ErrSectionWithoutBody},
		{"unhandled body kind", Section{Body: Body{kind: bodyKind(99)}}, ErrUnhandledBodyKind},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := json.Marshal(Descriptor{Version: CurrentDescriptorVersion, Sections: []Section{tc.s}})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestRefusalInOneCellFailsTheWholeDescriptor: a renderer never receives
// a payload with the bad cell dropped or blanked.
func TestRefusalInOneCellFailsTheWholeDescriptor(t *testing.T) {
	d := fixtureDescriptor()
	d.Sections[3] = Section{Body: NewDefinitionListBody(DefinitionListBody{Groups: []DefinitionGroup{{
		Entries: []DefinitionEntry{{Key: "k", Value: LinkCell("javascript:x", "x")}},
	}}})}
	b, err := json.Marshal(d)
	if !errors.Is(err, ErrUnsafeLink) || b != nil {
		t.Fatalf("Marshal = %s, %v; want nil bytes and ErrUnsafeLink", b, err)
	}
}

func TestValueContainingMarkupMarshalsAsAnOrdinaryJSONString(t *testing.T) {
	hostile := Value(`<script>alert(1)</script>"'&`)
	b, err := json.Marshal(oneCell(TextCell(hostile)))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got struct {
		Sections []struct {
			Body struct {
				Rows [][]struct {
					Text string `json:"text"`
				} `json:"rows"`
			} `json:"body"`
		} `json:"sections"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, b)
	}
	if got.Sections[0].Body.Rows[0][0].Text != string(hostile) {
		t.Errorf("round-tripped text = %q, want %q", got.Sections[0].Body.Rows[0][0].Text, hostile)
	}
}

func TestBodyMarshalledDirectlyRefuses(t *testing.T) {
	_, err := json.Marshal(NewTableBody(TableBody{Columns: []string{"a"}}))
	if !errors.Is(err, ErrBodyMarshalledDirectly) {
		t.Errorf("error = %v, want ErrBodyMarshalledDirectly", err)
	}
}

func TestEmbeddingBodyRefusesTheWholeEmbedder(t *testing.T) {
	type embedder struct {
		Body
		Name string `json:"name"`
	}
	_, err := json.Marshal(embedder{Name: "mine"})
	if !errors.Is(err, ErrBodyMarshalledDirectly) {
		t.Errorf("error = %v, want ErrBodyMarshalledDirectly", err)
	}
}

func TestDescriptorEncodeErrorsAreDistinct(t *testing.T) {
	errs := []error{
		ErrBodyMarshalledDirectly, ErrUnhandledBodyKind, ErrSectionWithoutBody, ErrZeroCell,
		ErrUnhandledCellKind, ErrUnknownBadge, ErrZeroTime, ErrUnsafeLink,
		ErrChartTooManySeries, ErrChartSeriesTooLong, ErrChartTooManyPoints, ErrChartTooManySections, ErrChartSeriesNoName,
		ErrChartValueNotFinite, ErrChartZeroTime, ErrChartTimeOutOfRange, ErrChartPointsOutOfOrder,
	}
	for i, a := range errs {
		for j, b := range errs {
			if i != j && errors.Is(a, b) {
				t.Errorf("%v matches %v", a, b)
			}
		}
	}
}
