package wadl

import (
	"encoding/xml"
	"net/http"
	"strings"
	"testing"
)

const bodyXSD = `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema">
  <xs:complexType name="Resource"/>
  <xs:complexType name="Obj"><xs:complexContent><xs:extension base="Resource"><xs:sequence>
    <xs:element name="mRID" minOccurs="1" maxOccurs="1" type="xs:string"/>
    <xs:element name="note" minOccurs="0" maxOccurs="1" type="xs:string"/>
  </xs:sequence></xs:extension></xs:complexContent></xs:complexType>
  <xs:complexType name="Thing"><xs:complexContent><xs:extension base="Obj"><xs:sequence>
    <xs:element name="Inner" type="Inner" minOccurs="1" maxOccurs="1"/>
    <xs:element name="Point" type="Point" minOccurs="1" maxOccurs="2"/>
  </xs:sequence></xs:extension></xs:complexContent></xs:complexType>
  <xs:complexType name="Inner"><xs:sequence>
    <xs:element name="a" minOccurs="1" maxOccurs="1" type="xs:int"/>
    <xs:element name="b" minOccurs="0" maxOccurs="1" type="xs:int"/>
  </xs:sequence></xs:complexType>
  <xs:complexType name="Point"><xs:sequence>
    <xs:element name="x" minOccurs="1" maxOccurs="1" type="xs:int"/>
  </xs:sequence></xs:complexType>
  <xs:element name="ThingListLink" type="Link"/>
  <xs:complexType name="Holder"><xs:sequence><xs:element name="ThingListLink" type="Link"/></xs:sequence></xs:complexType>
</xs:schema>`

func TestValidateBodyAgainstSequences(t *testing.T) {
	s, err := ParseLinkSchema([]byte(bodyXSD))
	if err != nil {
		t.Fatal(err)
	}
	ns := ` xmlns="` + sepNS + `"`
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"valid with optional omitted", `<Thing` + ns + `><mRID>AA</mRID><Inner><a>1</a></Inner><Point><x>1</x></Point></Thing>`, ""},
		{"valid with optionals and a repeat", `<Thing` + ns + `><mRID>AA</mRID><note>n</note><Inner><a>1</a><b>2</b></Inner><Point><x>1</x></Point><Point><x>2</x></Point></Thing>`, ""},
		{"empty element misses the base type's required child", `<Thing` + ns + `/>`, "mRID"},
		{"missing required nested child", `<Thing` + ns + `><mRID>AA</mRID><Inner><b>2</b></Inner><Point><x>1</x></Point></Thing>`, "a"},
		{"missing required trailing child", `<Thing` + ns + `><mRID>AA</mRID><Inner><a>1</a></Inner></Thing>`, "Point"},
		{"out of order", `<Thing` + ns + `><Inner><a>1</a></Inner><mRID>AA</mRID><Point><x>1</x></Point></Thing>`, "mRID"},
		{"undeclared child", `<Thing` + ns + `><mRID>AA</mRID><bogus/><Inner><a>1</a></Inner><Point><x>1</x></Point></Thing>`, "bogus"},
		{"repeat beyond maxOccurs", `<Thing` + ns + `><mRID>AA</mRID><Inner><a>1</a></Inner><Point><x>1</x></Point><Point><x>2</x></Point><Point><x>3</x></Point></Thing>`, "Point"},
		{"repeat of a maxOccurs 1 child", `<Thing` + ns + `><mRID>AA</mRID><mRID>BB</mRID><Inner><a>1</a></Inner><Point><x>1</x></Point></Thing>`, "mRID"},
		{"unknown root type", `<Nope` + ns + `/>`, "Nope"},
		{"not xml", `<Thing`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.ValidateBody([]byte(c.body))
			switch {
			case c.name == "not xml":
				if err == nil {
					t.Fatal("malformed XML validated")
				}
			case c.wantErr == "":
				if err != nil {
					t.Fatalf("valid body refused: %v", err)
				}
			default:
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error = %v, want one naming %q", err, c.wantErr)
				}
			}
		})
	}
}

// derBodies are the manager-only DER writes whose empty element the server
// rightly refuses with 400; the sweep must send a populated body for each.
var derBodies = []struct {
	id, verb, root string
	leaves         map[string]string
}{
	{"DERProgramList", http.MethodPost, "DERProgram", map[string]string{"primacy": "1"}},
	{"AssociatedDERProgramList", http.MethodPost, "DERProgram", map[string]string{"primacy": "1"}},
	{"DERControlList", http.MethodPost, "DERControl", map[string]string{"currentStatus": "0", "duration": "3600"}},
	{"DefaultDERControl", http.MethodPut, "DefaultDERControl", map[string]string{"value": "50"}},
	{"DERCurveList", http.MethodPost, "DERCurve", map[string]string{"curveType": "0", "xvalue": "0", "yvalue": "0"}},
}

func leafValues(t *testing.T, body []byte) map[string]string {
	t.Helper()
	out := map[string]string{}
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	var name string
	for {
		tok, err := dec.Token()
		if err != nil {
			return out
		}
		switch e := tok.(type) {
		case xml.StartElement:
			name = e.Name.Local
		case xml.CharData:
			if v := strings.TrimSpace(string(e)); v != "" && name != "" {
				out[name] = v
			}
		case xml.EndElement:
			name = ""
		}
	}
}

func TestRequestBodyForDERWritesIsPopulated(t *testing.T) {
	for _, c := range derBodies {
		t.Run(c.id, func(t *testing.T) {
			body := requestBody(Method{ResourceID: c.id, Verb: c.verb, RequestElements: []string{c.root}})
			if got := rootElement(body); got != c.root {
				t.Fatalf("root element = %q, want %q (body %s)", got, c.root, body)
			}
			if !strings.Contains(string(body), `xmlns="`+sepNS+`"`) {
				t.Fatalf("body is not in the 2030.5 namespace: %s", body)
			}
			leaves := leafValues(t, body)
			for k, want := range c.leaves {
				if leaves[k] != want {
					t.Errorf("%s = %q, want %q in %s", k, leaves[k], want, body)
				}
			}
			if leaves["mRID"] == "" || len(leaves["mRID"]) != 32 {
				t.Errorf("mRID = %q, want 32 hex digits", leaves["mRID"])
			}
		})
	}
}

func TestRequestBodyEverywhereElseStaysEmpty(t *testing.T) {
	empty := `<EndDevice xmlns="` + sepNS + `"/>`
	for _, verb := range []string{http.MethodPut, http.MethodPost} {
		got := requestBody(Method{ResourceID: "EndDeviceList", Verb: verb, RequestElements: []string{"EndDevice"}})
		if string(got) != empty {
			t.Errorf("%s on a resource with no entry sent %q, want %q", verb, got, empty)
		}
	}
	// The same resource under another verb is not in the table either.
	got := requestBody(Method{ResourceID: "DERControlList", Verb: http.MethodPut, RequestElements: []string{"DERControl"}})
	if string(got) != `<DERControl xmlns="`+sepNS+`"/>` {
		t.Errorf("PUT DERControlList sent %q, want the empty element", got)
	}
	for _, verb := range []string{http.MethodGet, http.MethodDelete, http.MethodHead} {
		if got := requestBody(Method{ResourceID: "DERControlList", Verb: verb, RequestElements: []string{"DERControl"}}); got != nil {
			t.Errorf("%s sent a body %q", verb, got)
		}
	}
	if got := requestBody(Method{ResourceID: "DERControlList", Verb: http.MethodPost}); got != nil {
		t.Errorf("a method declaring no request element sent %q", got)
	}
}

// TestDERWriteBodiesAreSchemaValid runs every writeBodies entry through the
// structural validator when a sep.xsd copy is supplied. The validator checks
// element presence, order and maxOccurs only, not simple-type values,
// attributes or facets; the full lexical check moves to core's exported
// validator (GRIDAPPSD/ieee-2030_5-core-go#256). The control drops a required
// element, so a pass cannot be a validator that accepts everything.
func TestDERWriteBodiesAreSchemaValid(t *testing.T) {
	links := SchemaForTest(t)
	if links == nil {
		t.Skip("no sep.xsd: the bodies are checked against the schema")
	}
	if len(writeBodies) == 0 {
		t.Fatal("writeBodies is empty, so nothing was checked")
	}
	for key, raw := range writeBodies {
		t.Run(key.resource, func(t *testing.T) {
			body := []byte(raw)
			if err := links.ValidateBody(body); err != nil {
				t.Fatalf("%s %s body is not schema-valid: %v\n%s", key.verb, key.resource, err, body)
			}
			start := strings.Index(raw, "<mRID>")
			end := strings.Index(raw, "</mRID>")
			if start < 0 || end < start {
				t.Fatalf("control: %s body has no mRID to drop: %s", key.resource, raw)
			}
			dropped := raw[:start] + raw[end+len("</mRID>"):]
			err := links.ValidateBody([]byte(dropped))
			if err == nil {
				t.Fatal("control: a body missing its required mRID validated, so the check cannot fail")
			}
			if !strings.Contains(err.Error(), "required mRID is missing") {
				t.Fatalf("control: error = %v, want the required-missing refusal for mRID", err)
			}
		})
	}
}

// TestDERWriteBodiesMatchTheWADL keeps the table's keys honest against the
// normative WADL: each key names a real optional write whose request may carry
// the element its body is rooted at. It iterates writeBodies itself, so a new
// entry is checked without a second list to update.
func TestDERWriteBodiesMatchTheWADL(t *testing.T) {
	m := MustLoadModel(t)
	for key, raw := range writeBodies {
		root := rootElement([]byte(raw))
		found := false
		for _, meth := range m.Methods {
			if meth.ResourceID == key.resource && meth.Verb == key.verb && containsStr(meth.RequestElements, root) {
				found = true
			}
		}
		if !found {
			t.Errorf("no WADL method %s %s accepts a %s request", key.verb, key.resource, root)
		}
	}
}
