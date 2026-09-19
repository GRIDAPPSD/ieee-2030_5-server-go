package sep2admin

import (
	"encoding/json"
	"testing"
)

// TestDescriptorMarshalsTableBody is the field-by-field proof for #368
// item 4: a renderer in another language reads this JSON by field name,
// so the test asserts every field's value rather than only that
// marshalling did not error, per data-invariants.md Rule 1.
func TestDescriptorMarshalsTableBody(t *testing.T) {
	d := Descriptor{
		Version: CurrentDescriptorVersion,
		Body: TableBody{
			Columns: []string{"mRID", "Name"},
			Rows: []Row{
				{"abc123", "device-1"},
				{"def456", "device-2"},
			},
		},
	}

	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got struct {
		Version int    `json:"version"`
		Kind    string `json:"kind"`
		Body    struct {
			Columns []string   `json:"columns"`
			Rows    [][]string `json:"rows"`
		} `json:"body"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal the marshalled bytes: %v\nbytes: %s", err, b)
	}

	if got.Version != CurrentDescriptorVersion {
		t.Errorf("version = %d, want %d", got.Version, CurrentDescriptorVersion)
	}
	if got.Kind != "table" {
		t.Errorf("kind = %q, want %q", got.Kind, "table")
	}
	wantColumns := []string{"mRID", "Name"}
	if len(got.Body.Columns) != len(wantColumns) {
		t.Fatalf("body.columns = %v, want %v", got.Body.Columns, wantColumns)
	}
	for i, c := range wantColumns {
		if got.Body.Columns[i] != c {
			t.Errorf("body.columns[%d] = %q, want %q", i, got.Body.Columns[i], c)
		}
	}
	wantRows := [][]string{{"abc123", "device-1"}, {"def456", "device-2"}}
	if len(got.Body.Rows) != len(wantRows) {
		t.Fatalf("body.rows = %v, want %v", got.Body.Rows, wantRows)
	}
	for i, row := range wantRows {
		if len(got.Body.Rows[i]) != len(row) {
			t.Fatalf("body.rows[%d] = %v, want %v", i, got.Body.Rows[i], row)
		}
		for j, cell := range row {
			if got.Body.Rows[i][j] != cell {
				t.Errorf("body.rows[%d][%d] = %q, want %q", i, j, got.Body.Rows[i][j], cell)
			}
		}
	}
}

// TestDescriptorMarshalsDefinitionListBody covers the multi-group shape:
// the bridge's ControlFlow panel renders two definition lists on one
// route (verified against gridappsd-ieee-2030_5-go's origin/main), so a
// DefinitionListBody with two groups is the case that shape exists for.
func TestDescriptorMarshalsDefinitionListBody(t *testing.T) {
	d := Descriptor{
		Version: CurrentDescriptorVersion,
		Body: DefinitionListBody{
			Groups: []DefinitionGroup{
				{
					Heading: "Connection topics",
					Entries: []DefinitionEntry{
						{Key: "Simulation output topic", Value: "goss/gridappsd/simulation/output"},
					},
				},
				{
					Heading: "Last applied delta",
					Entries: []DefinitionEntry{
						{Key: "Object", Value: "der-1"},
						{Key: "Attribute", Value: "wMax"},
					},
				},
			},
		},
	}

	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got struct {
		Version int    `json:"version"`
		Kind    string `json:"kind"`
		Body    struct {
			Groups []struct {
				Heading string `json:"heading"`
				Entries []struct {
					Key   string `json:"key"`
					Value string `json:"value"`
				} `json:"entries"`
			} `json:"groups"`
		} `json:"body"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal the marshalled bytes: %v\nbytes: %s", err, b)
	}

	if got.Kind != "definitionList" {
		t.Errorf("kind = %q, want %q", got.Kind, "definitionList")
	}
	if len(got.Body.Groups) != 2 {
		t.Fatalf("body.groups has %d groups, want 2", len(got.Body.Groups))
	}
	if got.Body.Groups[0].Heading != "Connection topics" {
		t.Errorf("body.groups[0].heading = %q, want %q", got.Body.Groups[0].Heading, "Connection topics")
	}
	if len(got.Body.Groups[0].Entries) != 1 || got.Body.Groups[0].Entries[0].Key != "Simulation output topic" ||
		got.Body.Groups[0].Entries[0].Value != "goss/gridappsd/simulation/output" {
		t.Errorf("body.groups[0].entries = %v, want one entry Simulation output topic=goss/gridappsd/simulation/output", got.Body.Groups[0].Entries)
	}
	if got.Body.Groups[1].Heading != "Last applied delta" {
		t.Errorf("body.groups[1].heading = %q, want %q", got.Body.Groups[1].Heading, "Last applied delta")
	}
	if len(got.Body.Groups[1].Entries) != 2 {
		t.Fatalf("body.groups[1].entries has %d entries, want 2", len(got.Body.Groups[1].Entries))
	}
	if got.Body.Groups[1].Entries[0].Key != "Object" || got.Body.Groups[1].Entries[0].Value != "der-1" {
		t.Errorf("body.groups[1].entries[0] = %+v, want Object=der-1", got.Body.Groups[1].Entries[0])
	}
	if got.Body.Groups[1].Entries[1].Key != "Attribute" || got.Body.Groups[1].Entries[1].Value != "wMax" {
		t.Errorf("body.groups[1].entries[1] = %+v, want Attribute=wMax", got.Body.Groups[1].Entries[1])
	}
}

// TestDescriptorWithNoBodyOmitsKindAndBody proves the "or none" half of
// item 2: a Descriptor with a nil Body marshals with no kind and no body
// key at all, not a null placeholder for either.
func TestDescriptorWithNoBodyOmitsKindAndBody(t *testing.T) {
	d := Descriptor{Version: CurrentDescriptorVersion}

	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal the marshalled bytes: %v\nbytes: %s", err, b)
	}

	if _, ok := got["kind"]; ok {
		t.Errorf("marshalled object has a %q key with no Body set, want it omitted: %s", "kind", b)
	}
	if _, ok := got["body"]; ok {
		t.Errorf("marshalled object has a %q key with no Body set, want it omitted: %s", "body", b)
	}
	versionRaw, ok := got["version"]
	if !ok {
		t.Fatalf("marshalled object has no %q key: %s", "version", b)
	}
	var version int
	if err := json.Unmarshal(versionRaw, &version); err != nil {
		t.Fatalf("unmarshal version: %v", err)
	}
	if version != CurrentDescriptorVersion {
		t.Errorf("version = %d, want %d", version, CurrentDescriptorVersion)
	}
}

// TestValueContainingMarkupMarshalsAsAnOrdinaryJSONString supports
// criterion 6's eventual renderer-side escaping test (issue #368 item 5:
// nothing here renders anything) by proving the type gives that
// obligation nowhere to slip at the marshalling boundary: a Value
// carrying a script tag, a quote, or an ampersand round-trips as the
// exact same text, not as embedded markup. What renders it as text on
// the page is a later issue's job; this test is the control that the
// wire payload never stops being plain data on the way there.
func TestValueContainingMarkupMarshalsAsAnOrdinaryJSONString(t *testing.T) {
	hostile := Value(`<script>alert(1)</script>"'&`)
	d := Descriptor{
		Version: CurrentDescriptorVersion,
		Body: TableBody{
			Columns: []string{"value"},
			Rows:    []Row{{hostile}},
		},
	}

	b, err := json.Marshal(d)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got struct {
		Body struct {
			Rows [][]string `json:"rows"`
		} `json:"body"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal the marshalled bytes: %v\nbytes: %s", err, b)
	}
	if len(got.Body.Rows) != 1 || len(got.Body.Rows[0]) != 1 {
		t.Fatalf("body.rows = %v, want one row of one cell", got.Body.Rows)
	}
	if got.Body.Rows[0][0] != string(hostile) {
		t.Errorf("round-tripped cell = %q, want %q: a hostile Value must survive as data, not be altered or interpreted", got.Body.Rows[0][0], string(hostile))
	}
}
