package wadl

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// childDecl is one element declaration inside a complex type's sequence.
type childDecl struct {
	name     string
	typ      string
	optional bool
	// max is maxOccurs; unbounded is -1.
	max int
}

// maxOccurs reads the attribute, defaulting an absent one to the schema's 1.
func maxOccurs(v string) int {
	switch v {
	case "":
		return 1
	case "unbounded":
		return -1
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 1
	}
	return n
}

// sequence returns the declarations an instance of the type carries, base type
// first, which is the order the schema requires them in.
func (s *LinkSchema) sequence(typ string) []childDecl {
	var chain []string
	for hops := 0; typ != "" && hops < 32; hops++ {
		chain = append(chain, typ)
		typ = s.bases[typ]
	}
	var out []childDecl
	for i := len(chain) - 1; i >= 0; i-- {
		out = append(out, s.children[chain[i]]...)
	}
	return out
}

// ValidateBody checks that an instance carries every required element of its
// root type, in schema order, with no undeclared element and no repeat past
// maxOccurs, recursing through the types the elements name.
//
// It is a structural check and no more: simple-type values, attributes and
// facets are not examined, so it cannot call a body valid, only refuse one that
// is certainly not. The sweep uses it to keep a fixture from sliding back to a
// body the server must reject.
func (s *LinkSchema) ValidateBody(body []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("reading body: %w", err)
		}
		if start, ok := tok.(xml.StartElement); ok {
			if _, known := s.children[start.Name.Local]; !known {
				return fmt.Errorf("root element %q is not a type sep.xsd declares", start.Name.Local)
			}
			return s.checkElement(dec, start.Name.Local, start.Name.Local)
		}
	}
}

// checkElement validates the content of an element whose start tag dec has
// just returned, through its end tag.
func (s *LinkSchema) checkElement(dec *xml.Decoder, path, typ string) error {
	decls := s.sequence(typ)
	last, count := -1, 0
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("%s: element not closed", path)
			}
			return fmt.Errorf("%s: %w", path, err)
		}
		switch e := tok.(type) {
		case xml.StartElement:
			name := e.Name.Local
			j := -1
			if last >= 0 && decls[last].name == name {
				if decls[last].max >= 0 && count >= decls[last].max {
					return fmt.Errorf("%s: %s appears more than the %d time(s) its maxOccurs allows", path, name, decls[last].max)
				}
				j = last
				count++
			} else {
				for k := last + 1; k < len(decls); k++ {
					if decls[k].name == name {
						j = k
						break
					}
					if !decls[k].optional {
						return fmt.Errorf("%s: required %s is missing or out of order before %s", path, decls[k].name, name)
					}
				}
			}
			if j < 0 {
				return fmt.Errorf("%s: %s is not declared here, or follows an element it must precede", path, name)
			}
			if j != last {
				last, count = j, 1
			}
			if err := s.checkElement(dec, path+"/"+name, decls[j].typ); err != nil {
				return err
			}
		case xml.EndElement:
			for k := last + 1; k < len(decls); k++ {
				if !decls[k].optional {
					return fmt.Errorf("%s: required %s is missing", path, decls[k].name)
				}
			}
			return nil
		}
	}
}
