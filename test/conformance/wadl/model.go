package wadl

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file turns the normative WADL into a machine-readable conformance
// model. It is a pure transform: no network, no server, deterministic
// ordering so a diff between two runs is meaningful.
//
// The WADL is normative per IEEE 2030.5-2018 Annex A.1 p.132. Every resource
// carries a full wx:samplePath, so the document is effectively flat: no
// nested resource composition is needed to recover a path.
//
// The point of parsing rather than transcribing is that the sweep is driven
// by what the standard declares, not by a list someone typed. A hand-written
// list can only find defects its author already suspected.

// Mode is the wx:mode conformance requirement level a method carries.
type Mode string

// The wx:mode values, straight from the WADL extension. Their meanings decide
// whether a live observation is a defect or a design choice, so they are
// named here rather than left as bare letters at the comparison site.
const (
	ModeMandatory  Mode = "M"
	ModeOptional   Mode = "O"
	ModeDeprecated Mode = "D"
	ModeError      Mode = "E"
)

// Meaning renders a mode for a human reading a report.
func (m Mode) Meaning() string {
	switch m {
	case ModeMandatory:
		return "Mandatory"
	case ModeOptional:
		return "Optional"
	case ModeDeprecated:
		return "Deprecated"
	case ModeError:
		return "Error (method must be refused)"
	default:
		return "unknown"
	}
}

// Method is one (resource, HTTP method) pair the WADL declares. It is the
// left-hand side of every conformance comparison the sweep makes.
type Method struct {
	ResourceID string
	// Path is the wx:samplePath, which may carry {idN} template slots.
	Path   string
	Verb   string
	ID     string
	Mode   Mode
	Detail string

	// RequestElements are the local element names the request may carry.
	RequestElements []string
	QueryParams     []string

	// DeclaredStatuses are the response status codes the WADL declares.
	// An empty slice means only the implicit success response is declared.
	DeclaredStatuses []int
	// DeclaredElements are the local element names a response may carry.
	DeclaredElements []string
	// RequiresLocationHeader records a response param of style "header"
	// named Location with required="true".
	RequiresLocationHeader bool
}

// Templated reports whether the path carries at least one {idN} slot.
func (m Method) Templated() bool { return strings.Contains(m.Path, "{") }

// ResourceSummary is one WADL resource and how many methods it declares.
type ResourceSummary struct {
	ID          string
	Path        string
	MethodCount int
}

// Model is the whole parsed WADL.
type Model struct {
	Resources []ResourceSummary
	Methods   []Method
}

// ModeCounts tallies methods by conformance mode. Reporting a single total
// would blur the distinction that decides whether a row matters: an unrouted
// Deprecated method is a design choice, an unrouted Mandatory method is a
// conformance failure.
func (m Model) ModeCounts() map[Mode]int {
	out := make(map[Mode]int, 4)
	for _, meth := range m.Methods {
		out[meth.Mode]++
	}
	return out
}

// ---- raw document shape -------------------------------------------------

type rawApplication struct {
	XMLName   xml.Name       `xml:"http://wadl.dev.java.net/2009/02 application"`
	Resources []rawResources `xml:"http://wadl.dev.java.net/2009/02 resources"`
	Resource  []rawResource  `xml:"http://wadl.dev.java.net/2009/02 resource"`
}

type rawResources struct {
	Resource []rawResource `xml:"http://wadl.dev.java.net/2009/02 resource"`
}

type rawResource struct {
	ID     string      `xml:"id,attr"`
	Attrs  []xml.Attr  `xml:",any,attr"`
	Method []rawMethod `xml:"http://wadl.dev.java.net/2009/02 method"`
	// Resources may nest. The 2030.5 WADL is flat in practice, but decoding
	// the nesting means a future edition that nests is parsed rather than
	// silently yielding zero methods for the nested subtree.
	Resource []rawResource `xml:"http://wadl.dev.java.net/2009/02 resource"`
}

type rawMethod struct {
	Name     string        `xml:"name,attr"`
	ID       string        `xml:"id,attr"`
	Attrs    []xml.Attr    `xml:",any,attr"`
	Doc      []rawDoc      `xml:"http://wadl.dev.java.net/2009/02 doc"`
	Request  *rawRequest   `xml:"http://wadl.dev.java.net/2009/02 request"`
	Response []rawResponse `xml:"http://wadl.dev.java.net/2009/02 response"`
}

type rawDoc struct {
	Title string `xml:"title,attr"`
	Body  string `xml:",chardata"`
}

type rawRequest struct {
	Param          []rawParam          `xml:"http://wadl.dev.java.net/2009/02 param"`
	Representation []rawRepresentation `xml:"http://wadl.dev.java.net/2009/02 representation"`
}

type rawResponse struct {
	Status         string              `xml:"status,attr"`
	Param          []rawParam          `xml:"http://wadl.dev.java.net/2009/02 param"`
	Representation []rawRepresentation `xml:"http://wadl.dev.java.net/2009/02 representation"`
}

type rawParam struct {
	Name     string `xml:"name,attr"`
	Style    string `xml:"style,attr"`
	Required string `xml:"required,attr"`
}

type rawRepresentation struct {
	MediaType string `xml:"mediaType,attr"`
	Element   string `xml:"element,attr"`
}

// attr finds an attribute by namespace URI and local name.
//
// The wx: attributes are looked up this way rather than through a namespaced
// struct tag because Go's encoding/xml resolves attribute prefixes
// inconsistently across shapes, and a silently-empty wx:mode would turn every
// method into mode "unknown" without any parse error to notice. Matching on
// the decoded Name is explicit and cannot fail quietly; the local-name
// fallback covers a decoder that left the prefix unresolved.
func attr(attrs []xml.Attr, space, local string) string {
	for _, a := range attrs {
		if a.Name.Local == local && a.Name.Space == space {
			return a.Value
		}
	}
	for _, a := range attrs {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// stripNS turns "sep:DERProgramList" into "DERProgramList".
//
// The sweep compares the root element of a live response body against this.
// Response bodies carry the sep namespace on the root element, so comparing
// local names is the honest comparison.
func stripNS(qname string) string {
	if qname == "" {
		return ""
	}
	if i := strings.LastIndex(qname, ":"); i >= 0 {
		return qname[i+1:]
	}
	return qname
}

// Parse builds the conformance model from WADL bytes.
func Parse(data []byte) (*Model, error) {
	var app rawApplication
	if err := xml.Unmarshal(data, &app); err != nil {
		return nil, fmt.Errorf("parsing WADL: %w", err)
	}

	m := &Model{}
	for _, rs := range app.Resources {
		for i := range rs.Resource {
			m.walk(&rs.Resource[i])
		}
	}
	for i := range app.Resource {
		m.walk(&app.Resource[i])
	}

	if len(m.Methods) == 0 {
		// A WADL that parses to nothing is a parser defect masquerading as
		// a clean sweep, so it is an error rather than an empty model.
		return nil, fmt.Errorf("WADL parsed to zero methods: the document shape is not what this parser expects")
	}

	sort.Slice(m.Methods, func(i, j int) bool {
		a, b := m.Methods[i], m.Methods[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.ResourceID != b.ResourceID {
			return a.ResourceID < b.ResourceID
		}
		return a.Verb < b.Verb
	})
	sort.Slice(m.Resources, func(i, j int) bool {
		a, b := m.Resources[i], m.Resources[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.ID < b.ID
	})
	return m, nil
}

func (m *Model) walk(r *rawResource) {
	samplePath := attr(r.Attrs, NamespaceExt, "samplePath")
	if samplePath != "" {
		m.Resources = append(m.Resources, ResourceSummary{
			ID:          r.ID,
			Path:        samplePath,
			MethodCount: len(r.Method),
		})
		for i := range r.Method {
			m.Methods = append(m.Methods, buildMethod(r.ID, samplePath, &r.Method[i]))
		}
	}
	for i := range r.Resource {
		m.walk(&r.Resource[i])
	}
}

func buildMethod(resourceID, path string, rm *rawMethod) Method {
	out := Method{
		ResourceID: resourceID,
		Path:       path,
		Verb:       strings.ToUpper(rm.Name),
		ID:         rm.ID,
		Mode:       Mode(attr(rm.Attrs, NamespaceExt, "mode")),
	}
	for _, d := range rm.Doc {
		if t := strings.TrimSpace(d.Title); t != "" {
			out.Detail = t
			break
		}
	}

	if rm.Request != nil {
		elems := map[string]bool{}
		for _, rep := range rm.Request.Representation {
			if e := stripNS(rep.Element); e != "" {
				elems[e] = true
			}
		}
		out.RequestElements = sortedKeys(elems)
		for _, p := range rm.Request.Param {
			if p.Style == "query" {
				out.QueryParams = append(out.QueryParams, p.Name)
			}
		}
	}

	statuses := map[int]bool{}
	elems := map[string]bool{}
	for _, resp := range rm.Response {
		// A response with no @status is the implicit success response.
		if resp.Status != "" {
			for _, f := range strings.Fields(resp.Status) {
				if n, err := strconv.Atoi(f); err == nil {
					statuses[n] = true
				}
			}
		}
		for _, rep := range resp.Representation {
			if e := stripNS(rep.Element); e != "" {
				elems[e] = true
			}
		}
		for _, p := range resp.Param {
			if strings.EqualFold(p.Style, "header") && strings.EqualFold(p.Name, "Location") {
				out.RequiresLocationHeader = true
			}
		}
	}
	for s := range statuses {
		out.DeclaredStatuses = append(out.DeclaredStatuses, s)
	}
	sort.Ints(out.DeclaredStatuses)
	out.DeclaredElements = sortedKeys(elems)
	return out
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
