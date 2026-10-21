package sep2

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// DERCurveLink references a DERCurve resource by URI. IEEE 2030.5-2018
// sep.xsd DERCurveLink, an extension of Link: an empty element whose
// required href attribute is the curve's URI.
type DERCurveLink struct {
	Href string `xml:"href,attr"`
}

var errEmptyCurveHref = errors.New("DERCurveLink: href is required")

// collapseURI applies xs:anyURI's whiteSpace="collapse" facet, so the href
// stored and re-emitted is the value a schema-aware parser would read.
func collapseURI(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// MarshalXML refuses an empty href, which the schema requires.
func (l DERCurveLink) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	href := collapseURI(l.Href)
	if href == "" {
		return fmt.Errorf("sep2: %s: %w", start.Name.Local, errEmptyCurveHref)
	}
	start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "href"}, Value: href})
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	return e.EncodeToken(start.End())
}

// UnmarshalXML requires the href and refuses any content, so the pre-#153
// integer form (<opModVoltVar>3</opModVoltVar>) errors instead of decoding
// as a link with no href.
func (l *DERCurveLink) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var href string
	for _, a := range start.Attr {
		if a.Name.Space == "" && a.Name.Local == "href" {
			href = collapseURI(a.Value)
		}
	}
	if href == "" {
		return fmt.Errorf("sep2: %s: %w", start.Name.Local, errEmptyCurveHref)
	}
	if err := decodeChildren(d, start, "DERCurveLink", nil, nil); err != nil {
		return err
	}
	l.Href = href
	return nil
}

// MarshalJSON mirrors MarshalXML's href guard.
func (l DERCurveLink) MarshalJSON() ([]byte, error) {
	href := collapseURI(l.Href)
	if href == "" {
		return nil, fmt.Errorf("sep2: %w", errEmptyCurveHref)
	}
	type plain DERCurveLink
	return json.Marshal(plain{Href: href})
}

// UnmarshalJSON applies the XML rules to the JSON server-go persists, so a
// stored integer curve ref fails to load.
func (l *DERCurveLink) UnmarshalJSON(data []byte) error {
	var raw struct{ Href *string }
	if err := decodeJSONStrict(data, &raw); err != nil {
		return fmt.Errorf("sep2: DERCurveLink: %w", err)
	}
	if raw.Href == nil || collapseURI(*raw.Href) == "" {
		return fmt.Errorf("sep2: %w", errEmptyCurveHref)
	}
	l.Href = collapseURI(*raw.Href)
	return nil
}

// FixedVar is the opModFixedVar setting: reactive power as a signed
// percentage, in hundredths of a percent, of the quantity RefType names.
// IEEE 2030.5-2018 sep.xsd FixedVar; RefType is a DERUnitRefType (UInt8).
type FixedVar struct {
	RefType uint8         `xml:"refType"`
	Value   SignedPerCent `xml:"value"`
}

// checkRefType enforces the opModFixedVar rule that refType SHALL be
// %setMaxW (1), %setMaxVar (2) or %statVarAvail (3); FixedVar is used
// nowhere else in the schema.
func checkRefType(r uint8) error {
	if r < 1 || r > 3 {
		return fmt.Errorf("sep2: FixedVar: refType %d is not 1 (%%setMaxW), 2 (%%setMaxVar) or 3 (%%statVarAvail)", r)
	}
	return nil
}

// MarshalXML refuses a refType the schema forbids.
func (f FixedVar) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if err := checkRefType(f.RefType); err != nil {
		return err
	}
	type plain FixedVar
	return e.EncodeElement(plain(f), start)
}

// MarshalJSON mirrors MarshalXML's refType guard.
func (f FixedVar) MarshalJSON() ([]byte, error) {
	if err := checkRefType(f.RefType); err != nil {
		return nil, err
	}
	type plain FixedVar
	return json.Marshal(plain(f))
}

// UnmarshalXML requires refType then value. The default decoder would read
// the pre-#153 ReactivePower form (multiplier, value) as refType 0.
func (f *FixedVar) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v FixedVar
	err := decodeChildren(d, start, "FixedVar", []string{"refType", "value"}, func(name, text string) error {
		if name == "refType" {
			n, err := parseUint(text, 8)
			v.RefType = uint8(n)
			return err
		}
		var err error
		v.Value, err = parseSignedPerCent(text)
		return err
	})
	if err != nil {
		return err
	}
	if err := checkRefType(v.RefType); err != nil {
		return err
	}
	*f = v
	return nil
}

// UnmarshalJSON requires both fields and refuses unknown ones, for the same
// reason as UnmarshalXML.
func (f *FixedVar) UnmarshalJSON(data []byte) error {
	var raw struct {
		RefType *uint8
		Value   *SignedPerCent
	}
	if err := decodeJSONStrict(data, &raw); err != nil {
		return fmt.Errorf("sep2: FixedVar: %w", err)
	}
	if err := requireJSON("FixedVar", jsonField{"RefType", raw.RefType == nil}, jsonField{"Value", raw.Value == nil}); err != nil {
		return err
	}
	if err := checkRefType(*raw.RefType); err != nil {
		return err
	}
	*f = FixedVar{RefType: *raw.RefType, Value: *raw.Value}
	return nil
}

// FreqDroopType is the opModFreqDroop setting. IEEE 2030.5-2018 sep.xsd
// FreqDroopType; every element is required.
type FreqDroopType struct {
	DBOF        uint32 `xml:"dBOF"`
	DBUF        uint32 `xml:"dBUF"`
	KOF         uint16 `xml:"kOF"`
	KUF         uint16 `xml:"kUF"`
	OpenLoopTms uint16 `xml:"openLoopTms"`
}

// UnmarshalXML requires all five elements in schema order. The default
// decoder would read the pre-#153 scalar form as all zeros.
func (f *FreqDroopType) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v FreqDroopType
	err := decodeChildren(d, start, "FreqDroopType", []string{"dBOF", "dBUF", "kOF", "kUF", "openLoopTms"}, func(name, text string) error {
		var err error
		var n uint64
		switch name {
		case "dBOF", "dBUF":
			n, err = parseUint(text, 32)
		default:
			n, err = parseUint(text, 16)
		}
		switch name {
		case "dBOF":
			v.DBOF = uint32(n)
		case "dBUF":
			v.DBUF = uint32(n)
		case "kOF":
			v.KOF = uint16(n)
		case "kUF":
			v.KUF = uint16(n)
		case "openLoopTms":
			v.OpenLoopTms = uint16(n)
		}
		return err
	})
	if err != nil {
		return err
	}
	*f = v
	return nil
}

// UnmarshalJSON requires all five fields and refuses unknown ones.
func (f *FreqDroopType) UnmarshalJSON(data []byte) error {
	var raw struct {
		DBOF, DBUF            *uint32
		KOF, KUF, OpenLoopTms *uint16
	}
	if err := decodeJSONStrict(data, &raw); err != nil {
		return fmt.Errorf("sep2: FreqDroopType: %w", err)
	}
	if err := requireJSON("FreqDroopType",
		jsonField{"DBOF", raw.DBOF == nil}, jsonField{"DBUF", raw.DBUF == nil}, jsonField{"KOF", raw.KOF == nil},
		jsonField{"KUF", raw.KUF == nil}, jsonField{"OpenLoopTms", raw.OpenLoopTms == nil}); err != nil {
		return err
	}
	*f = FreqDroopType{DBOF: *raw.DBOF, DBUF: *raw.DBUF, KOF: *raw.KOF, KUF: *raw.KUF, OpenLoopTms: *raw.OpenLoopTms}
	return nil
}

// decodeChildren consumes the rest of start, whose content must be exactly
// the simple-typed children in want, in order, each once, in start's
// namespace, with no other text. set receives each child's text.
func decodeChildren(d *xml.Decoder, start xml.StartElement, typeName string, want []string, set func(name, text string) error) error {
	next := 0
	for {
		tok, err := d.Token()
		if err != nil {
			return fmt.Errorf("sep2: %s: %w", typeName, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if next >= len(want) || t.Name.Local != want[next] {
				return fmt.Errorf("sep2: %s: unexpected element <%s>", typeName, t.Name.Local)
			}
			if t.Name.Space != start.Name.Space {
				return fmt.Errorf("sep2: %s: element <%s> in namespace %q, want %q", typeName, t.Name.Local, t.Name.Space, start.Name.Space)
			}
			text, err := simpleText(d)
			if err != nil {
				return fmt.Errorf("sep2: %s.%s: %w", typeName, t.Name.Local, err)
			}
			if err := set(t.Name.Local, text); err != nil {
				return fmt.Errorf("sep2: %s.%s: %w", typeName, t.Name.Local, err)
			}
			next++
		case xml.CharData:
			if len(bytes.TrimSpace(t)) > 0 {
				return fmt.Errorf("sep2: %s: unexpected text %q", typeName, bytes.TrimSpace(t))
			}
		case xml.EndElement:
			if next < len(want) {
				return fmt.Errorf("sep2: %s: missing element <%s>", typeName, want[next])
			}
			return nil
		}
	}
}

// simpleText reads a simple-typed element's text up to its end tag. Unlike
// DecodeElement into a string, it refuses a nested element rather than
// dropping it.
func simpleText(d *xml.Decoder) (string, error) {
	var text []byte
	for {
		tok, err := d.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			return "", fmt.Errorf("unexpected child element <%s>", t.Name.Local)
		case xml.CharData:
			text = append(text, t...)
		case xml.EndElement:
			return string(text), nil
		}
	}
}

type jsonField struct {
	name    string
	missing bool
}

// requireJSON names the first missing field.
func requireJSON(typeName string, fields ...jsonField) error {
	for _, f := range fields {
		if f.missing {
			return fmt.Errorf("sep2: %s: %s is required", typeName, f.name)
		}
	}
	return nil
}

func parseUint(text string, bits int) (uint64, error) {
	return strconv.ParseUint(strings.TrimSpace(text), 10, bits)
}

func decodeJSONStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
