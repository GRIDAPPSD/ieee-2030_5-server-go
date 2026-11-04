package sep2

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

// Expected child elements per quantity shape.
var (
	mulVal     = []string{"multiplier", "value"}
	dispMul    = []string{"displacement", "multiplier"}
	dispExcMul = []string{"displacement", "excitation", "multiplier"}
)

// decodeQuantity decodes a multiplier-bearing element into dst (a pointer to
// a method-free copy of the type). It refuses, through the shared refusals in
// content_shape.go, character data directly inside the element and a child
// element inside one of the named simple children; the default decoder reads
// both as zero with no error. fields are the expected child names, used for
// the error and to tell a simple child from an unknown one. An empty element
// and the complex form decode as before.
func decodeQuantity(d *xml.Decoder, start xml.StartElement, typeName string, fields []string, dst any) error {
	g := &quantityTokens{d: d, pending: &start, fields: fields}
	// Decode into a copy so a refused element leaves dst untouched.
	tmp := reflect.New(reflect.TypeOf(dst).Elem())
	err := xml.NewTokenDecoder(g).Decode(tmp.Interface())
	switch {
	case g.text != nil:
		return errUnexpectedText(typeName+" element <"+start.Name.Local+">", g.text, "children <"+strings.Join(fields, ">, <")+">")
	case g.child != "":
		return fmt.Errorf("sep2: %s element <%s>: <%s>: %w", typeName, start.Name.Local, g.parent, errUnexpectedChild(g.child))
	case err != nil:
		return fmt.Errorf("sep2: %s: %w", typeName, err)
	}
	reflect.ValueOf(dst).Elem().Set(tmp.Elem())
	return nil
}

var errQuantityShape = errors.New("wrong content shape")

// quantityTokens replays the start element, then forwards d's tokens until
// the element's content has the wrong shape, recording what it saw.
type quantityTokens struct {
	d       *xml.Decoder
	pending *xml.StartElement
	fields  []string
	depth   int
	parent  string // the depth-2 element last opened
	inField bool
	text    []byte // first non-blank text directly inside the element
	child   string // first child element inside a named simple child
}

func (g *quantityTokens) Token() (xml.Token, error) {
	if g.pending != nil {
		t := *g.pending
		g.pending = nil
		g.depth = 1
		return t, nil
	}
	tok, err := g.d.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case xml.StartElement:
		g.depth++
		if g.depth == 2 {
			g.parent = t.Name.Local
			g.inField = slices.Contains(g.fields, g.parent)
		} else if g.depth == 3 && g.inField {
			g.child = t.Name.Local
			return nil, errQuantityShape
		}
	case xml.EndElement:
		g.depth--
	case xml.CharData:
		if text := bytes.TrimSpace(t); g.depth == 1 && len(text) > 0 {
			g.text = bytes.Clone(text)
			return nil, errQuantityShape
		}
	}
	return tok, nil
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *ActivePower) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain ActivePower
	return decodeQuantity(d, start, "ActivePower", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *ReactivePower) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain ReactivePower
	return decodeQuantity(d, start, "ReactivePower", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *ApparentPower) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain ApparentPower
	return decodeQuantity(d, start, "ApparentPower", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *VoltageRMS) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain VoltageRMS
	return decodeQuantity(d, start, "VoltageRMS", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *CurrentRMS) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain CurrentRMS
	return decodeQuantity(d, start, "CurrentRMS", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *AmpereHour) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain AmpereHour
	return decodeQuantity(d, start, "AmpereHour", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *WattHour) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain WattHour
	return decodeQuantity(d, start, "WattHour", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *ReactiveSusceptance) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain ReactiveSusceptance
	return decodeQuantity(d, start, "ReactiveSusceptance", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *RealEnergy) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain RealEnergy
	return decodeQuantity(d, start, "RealEnergy", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *SignedRealEnergy) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain SignedRealEnergy
	return decodeQuantity(d, start, "SignedRealEnergy", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *Temperature) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain Temperature
	return decodeQuantity(d, start, "Temperature", mulVal, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *PowerFactor) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain PowerFactor
	return decodeQuantity(d, start, "PowerFactor", dispMul, (*plain)(q))
}

// UnmarshalXML refuses a bare-integer element instead of reading it as zero.
func (q *FixedPowerFactor) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	type plain FixedPowerFactor
	return decodeQuantity(d, start, "FixedPowerFactor", dispExcMul, (*plain)(q))
}
