package wadl

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"testing"
)

// This file decides which WADL resources the link crawl reached. IEEE 2030.5
// treats the WADL sample paths as recommendations: a client finds a resource by
// following links from /dcap, so a resource the server serves at its own href is
// conformant when a link leads to it, and a resource nothing links to is not
// reached, whatever answers at its sample path.

// xsiNS is the XML Schema instance namespace that carries xsi:type.
const xsiNS = "http://www.w3.org/2001/XMLSchema-instance"

const (
	// EnvSchemaPath names the sep.xsd copy the crawl reads Link fields from. It
	// is the variable core's schema gate reads, so one setting serves both.
	EnvSchemaPath = "SEP2_SCHEMA_PATH"
	// EnvSchemaRequired turns an absent schema into a failure, as EnvRequired
	// does for the WADL.
	EnvSchemaRequired = "SEP2_SCHEMA_REQUIRED"
)

// ErrSchemaNotFound reports that no sep.xsd copy is configured.
var ErrSchemaNotFound = errors.New("IEEE 2030.5 schema (sep.xsd) not available: set " + EnvSchemaPath + " to a copy")

// LinkSchema is what the crawl needs from sep.xsd: which Link fields some type
// declares, and which types extend which.
type LinkSchema struct {
	fields map[string]bool
	bases  map[string]string
}

// ParseLinkSchema reads Link fields and type extensions out of sep.xsd bytes.
//
// A Link field is an element named for a link and declared inside a complex
// type. A global element of the same name does not count: NotificationListLink
// exists only as a global element, no type carries it, and no crawl can reach
// the resource through it.
func ParseLinkSchema(xsd []byte) (*LinkSchema, error) {
	s := &LinkSchema{fields: map[string]bool{}, bases: map[string]string{}}
	dec := xml.NewDecoder(bytes.NewReader(xsd))
	var typeName string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing sep.xsd: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := attrValue(t.Attr, "name")
			switch t.Name.Local {
			case "complexType":
				typeName = name
			case "extension":
				if typeName != "" {
					s.bases[typeName] = stripNS(attrValue(t.Attr, "base"))
				}
			case "element":
				if typeName != "" && strings.HasSuffix(name, "Link") {
					s.fields[name] = true
				}
			}
		case xml.EndElement:
			if t.Name.Local == "complexType" {
				typeName = ""
			}
		}
	}
	if len(s.fields) == 0 {
		return nil, errors.New("sep.xsd parsed to zero Link fields: the document shape is not what this parser expects")
	}
	return s, nil
}

// Names reports whether a link in the schema leads to the resource: the resource's
// own Link field, its list's, or one of its base types'. A subtype is named
// through its base because a list member can carry it as xsi:type.
func (s *LinkSchema) Names(id string) bool {
	for hops := 0; id != "" && hops < 32; hops++ {
		if s.fields[id+"Link"] || s.fields[id+"ListLink"] {
			return true
		}
		id = s.bases[id]
	}
	return false
}

// LoadLinkSchema reads the schema named by EnvSchemaPath.
func LoadLinkSchema() (*LinkSchema, error) {
	p := strings.TrimSpace(os.Getenv(EnvSchemaPath))
	if p == "" {
		return nil, ErrSchemaNotFound
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading %s=%q: %w", EnvSchemaPath, p, err)
	}
	return ParseLinkSchema(data)
}

// SchemaForTest returns the schema, or nil when none is configured and none is
// required. A nil schema makes the sweep strict: every resource the crawl misses
// is not_reached, and none is probed at its sample path.
func SchemaForTest(t *testing.T) *LinkSchema {
	t.Helper()
	s, err := LoadLinkSchema()
	switch {
	case err == nil:
		return s
	case errors.Is(err, ErrSchemaNotFound):
		if required, perr := envRequired(EnvSchemaRequired); perr != nil {
			t.Fatalf("schema gate configuration: %v", perr)
		} else if required {
			t.Fatalf("%s is set but no schema is available: %v", EnvSchemaRequired, err)
		}
		t.Logf("no sep.xsd configured: schema-unlinked resources are not probed at their sample path")
		return nil
	default:
		t.Fatalf("load IEEE 2030.5 schema: %v", err)
	}
	return nil
}

func attrValue(attrs []xml.Attr, local string) string {
	for _, a := range attrs {
		if a.Name.Local == local && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}

// crawl records which resource ids the walk from /dcap reached, and at which hrefs.
type crawl struct {
	hrefs map[string][]string
}

func newCrawl() *crawl { return &crawl{hrefs: map[string][]string{}} }

func (c *crawl) add(id, href string) {
	if id == "" {
		return
	}
	for _, h := range c.hrefs[id] {
		if h == href {
			return
		}
	}
	c.hrefs[id] = append(c.hrefs[id], href)
}

// href is the first href that reached the resource.
func (c *crawl) href(id string) (string, bool) {
	hs := c.hrefs[id]
	if len(hs) == 0 {
		return "", false
	}
	return hs[0], true
}

// resourceID is the WADL resource id a followed link names: the xsi:type when a
// list member carries one, otherwise the element name without its Link suffix.
func (l link) resourceID() string {
	switch {
	case l.typeName != "":
		return l.typeName
	case l.linkElement != "":
		return strings.TrimSuffix(l.linkElement, "Link")
	}
	return l.expectedRoot
}

// rootTypeName is the local name of the root element's xsi:type, or "".
func rootTypeName(body []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			for _, a := range se.Attr {
				if a.Name.Local == "type" && a.Name.Space == xsiNS {
					return stripNS(a.Value)
				}
			}
			return ""
		}
	}
}

// reachState is how the crawl treats one WADL resource that has a Mandatory or
// Optional method.
type reachState int

const (
	// stateReached: a followed link led to the resource.
	stateReached reachState = iota + 1
	// stateNotReached: the schema names the resource and the crawl did not find it.
	stateNotReached
	// stateSamplePath: no schema link names the resource, so it is probed at its sample path.
	stateSamplePath
)

type resKey struct{ id, path string }

func keyOf(m Method) resKey { return resKey{m.ResourceID, m.Path} }

func requirable(m Mode) bool { return m == ModeMandatory || m == ModeOptional }

// resourceStates classifies every resource with a Mandatory or Optional method
// and returns one not_reached row per resource the crawl missed, in a stable order.
func resourceStates(m *Model, c *crawl, links *LinkSchema) (map[resKey]reachState, []Row) {
	best := map[resKey]Mode{}
	for _, meth := range m.Methods {
		if !requirable(meth.Mode) {
			continue
		}
		k := keyOf(meth)
		if best[k] != ModeMandatory {
			best[k] = meth.Mode
		}
	}
	keys := make([]resKey, 0, len(best))
	for k := range best {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].path != keys[j].path {
			return keys[i].path < keys[j].path
		}
		return keys[i].id < keys[j].id
	})

	states := make(map[resKey]reachState, len(keys))
	var rows []Row
	for _, k := range keys {
		switch {
		case len(c.hrefs[k.id]) > 0:
			states[k] = stateReached
		case links != nil && !links.Names(k.id):
			states[k] = stateSamplePath
		default:
			states[k] = stateNotReached
			rows = append(rows, Row{
				Phase:      PhaseWADL,
				ResourceID: k.id,
				WADLPath:   k.path,
				Path:       k.path,
				Mode:       best[k],
				Verdict:    VerdictNotReached,
			})
		}
	}
	return states, rows
}
