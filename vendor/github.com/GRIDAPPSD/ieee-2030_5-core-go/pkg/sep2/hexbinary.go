package sep2

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// This file defines the sep.xsd hexBinary family as Go types that own their
// own wire encoding.
//
// Why a dedicated type rather than a plain integer: sep.xsd models every one
// of these fields as a restriction of xs:hexBinary, so the lexical form on
// the wire is hex text, not decimal text. A plain uint32 field marshalled by
// encoding/xml renders as decimal, and a peer that parses the element as
// hexBinary (as the EPRI reference client does, xml_parse.c:86 dispatching
// to parse_hex) reads a DIFFERENT NUMBER without raising any error. The
// decimal text "2048" is valid hexBinary text, so nothing fails: it simply
// decodes as 0x2048, which is 8264. Value corruption of that shape passes
// every check on both ends, so the encoding has to be carried by the type
// rather than re-decided at each field.
//
// Lexical form produced here: uppercase hex, padded with a single leading
// "0" only when the natural digit count is odd.
//
//   - Whole octets are mandatory. sep.xsd:6241 states the rule verbatim:
//     "hexBinary requires pairs of hex characters, so an odd number of
//     characters requires a leading 0". The EPRI reference parser enforces
//     it: parse_hex (xml_parse.c:54-70) reads digits in pairs and returns
//     failure at line 62 when the second nibble of a pair is missing, so a
//     one-nibble "8" is a parse error, not a lenient read.
//
//   - Full-width zero padding is NOT applied. The maxLength facet counts
//     OCTETS, not characters: HexBinary32 is maxLength 4 (sep.xsd:6259),
//     which is 4 octets, i.e. up to 8 hex characters, and the type's own
//     documentation (sep.xsd:6257) says "8 hex characters max". Shorter is
//     therefore conformant. Emitting the minimal even-length form also
//     matches what the EPRI client itself emits: output_hex
//     (xml_output.c:51-60) skips leading zero octets before writing. Its
//     parser right-aligns a short value into the wider buffer and zero-fills
//     the front (xml_parse.c:65-68), so a minimal-form value decodes to the
//     identical number. Padding rule and numeric value are independent.
//
//   - Uppercase is the xs:hexBinary canonical lexical form. The EPRI client
//     accepts either case on input (hex_digit, xml_token.c:61-66) and emits
//     lowercase, so uppercase is safe on the wire and canonical in the
//     schema's terms.
//
// Numeric semantics are unchanged by these types: a field holding 2048 still
// holds 2048, it just appears on the wire as 0800.

// HexBinary8 is an 8-bit bitmap serialized as hexBinary (sep.xsd:6239-6246,
// maxLength 1 octet, exactly 2 hex characters).
type HexBinary8 uint8

// HexBinary16 is a 16-bit bitmap serialized as hexBinary (sep.xsd:6247-6254,
// maxLength 2 octets, up to 4 hex characters).
type HexBinary16 uint16

// HexBinary32 is a 32-bit bitmap serialized as hexBinary (sep.xsd:6255-6262,
// maxLength 4 octets, up to 8 hex characters).
type HexBinary32 uint32

// HexBinary64 is a 64-bit bitmap serialized as hexBinary (sep.xsd:6271-6278,
// maxLength 8 octets, up to 16 hex characters).
type HexBinary64 uint64

// hexBinaryText renders v as uppercase hex, left-padded with a single "0"
// when the natural hex representation has an odd digit count. See the file
// comment for why the padding stops at the octet boundary rather than
// filling the type's full width.
func hexBinaryText(v uint64) string {
	s := strings.ToUpper(strconv.FormatUint(v, 16))
	if len(s)%2 != 0 {
		s = "0" + s
	}
	return s
}

// hexBinaryValue parses hexBinary lexical text into a numeric value that
// fits in bits bits. It is the exact inverse of hexBinaryText for any value
// hexBinaryText can produce.
//
// Odd-length input is accepted even though sep.xsd forbids it. A peer that
// sends "800" instead of "0800" is not schema-conformant, but its intent is
// unambiguous and rejecting it would turn a peer's cosmetic defect into a
// dropped resource. What is NOT accepted, and must not be, is anything whose
// numeric reading is ambiguous or out of range: non-hex characters, an empty
// element, and values wider than the declared type all fail loudly rather
// than decoding to a silently wrong number.
func hexBinaryValue(text string, bits int) (uint64, error) {
	s := strings.TrimSpace(text)
	if s == "" {
		return 0, fmt.Errorf("sep2: empty hexBinary%d value", bits)
	}
	v, err := strconv.ParseUint(s, 16, bits)
	if err != nil {
		return 0, fmt.Errorf("sep2: parsing hexBinary%d value %q: %w", bits, s, err)
	}
	return v, nil
}

// MarshalXML renders the value as even-length uppercase hex text.
func (h HexBinary8) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(hexBinaryText(uint64(h)), start)
}

// UnmarshalXML parses hexBinary element text back to the numeric value.
func (h *HexBinary8) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var text string
	if err := d.DecodeElement(&text, &start); err != nil {
		return err
	}
	v, err := hexBinaryValue(text, 8)
	if err != nil {
		return err
	}
	*h = HexBinary8(v)
	return nil
}

// MarshalXMLAttr renders the value as even-length uppercase hex attribute
// text. sep.xsd places responseRequired (HexBinary8) in attribute position
// on RespondableResource (sep.xsd:5440), and encoding/xml formats an
// attribute-tagged integer as decimal unless the type implements this
// interface, which is the same corruption this file exists to prevent. Every
// member of the family carries the attribute methods so that moving a field
// between element and attribute position cannot silently change its encoding.
func (h HexBinary8) MarshalXMLAttr(name xml.Name) (xml.Attr, error) {
	return xml.Attr{Name: name, Value: hexBinaryText(uint64(h))}, nil
}

// UnmarshalXMLAttr parses hexBinary attribute text back to the numeric value.
func (h *HexBinary8) UnmarshalXMLAttr(attr xml.Attr) error {
	v, err := hexBinaryValue(attr.Value, 8)
	if err != nil {
		return err
	}
	*h = HexBinary8(v)
	return nil
}

// MarshalXML renders the value as even-length uppercase hex text.
func (h HexBinary16) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(hexBinaryText(uint64(h)), start)
}

// UnmarshalXML parses hexBinary element text back to the numeric value.
func (h *HexBinary16) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var text string
	if err := d.DecodeElement(&text, &start); err != nil {
		return err
	}
	v, err := hexBinaryValue(text, 16)
	if err != nil {
		return err
	}
	*h = HexBinary16(v)
	return nil
}

// MarshalXMLAttr renders the value as even-length uppercase hex attribute text.
func (h HexBinary16) MarshalXMLAttr(name xml.Name) (xml.Attr, error) {
	return xml.Attr{Name: name, Value: hexBinaryText(uint64(h))}, nil
}

// UnmarshalXMLAttr parses hexBinary attribute text back to the numeric value.
func (h *HexBinary16) UnmarshalXMLAttr(attr xml.Attr) error {
	v, err := hexBinaryValue(attr.Value, 16)
	if err != nil {
		return err
	}
	*h = HexBinary16(v)
	return nil
}

// MarshalXML renders the value as even-length uppercase hex text.
func (h HexBinary32) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(hexBinaryText(uint64(h)), start)
}

// UnmarshalXML parses hexBinary element text back to the numeric value.
func (h *HexBinary32) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var text string
	if err := d.DecodeElement(&text, &start); err != nil {
		return err
	}
	v, err := hexBinaryValue(text, 32)
	if err != nil {
		return err
	}
	*h = HexBinary32(v)
	return nil
}

// MarshalXMLAttr renders the value as even-length uppercase hex attribute text.
func (h HexBinary32) MarshalXMLAttr(name xml.Name) (xml.Attr, error) {
	return xml.Attr{Name: name, Value: hexBinaryText(uint64(h))}, nil
}

// UnmarshalXMLAttr parses hexBinary attribute text back to the numeric value.
func (h *HexBinary32) UnmarshalXMLAttr(attr xml.Attr) error {
	v, err := hexBinaryValue(attr.Value, 32)
	if err != nil {
		return err
	}
	*h = HexBinary32(v)
	return nil
}

// MarshalXML renders the value as even-length uppercase hex text.
func (h HexBinary64) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return e.EncodeElement(hexBinaryText(uint64(h)), start)
}

// UnmarshalXML parses hexBinary element text back to the numeric value.
func (h *HexBinary64) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var text string
	if err := d.DecodeElement(&text, &start); err != nil {
		return err
	}
	v, err := hexBinaryValue(text, 64)
	if err != nil {
		return err
	}
	*h = HexBinary64(v)
	return nil
}

// MarshalXMLAttr renders the value as even-length uppercase hex attribute text.
func (h HexBinary64) MarshalXMLAttr(name xml.Name) (xml.Attr, error) {
	return xml.Attr{Name: name, Value: hexBinaryText(uint64(h))}, nil
}

// UnmarshalXMLAttr parses hexBinary attribute text back to the numeric value.
func (h *HexBinary64) UnmarshalXMLAttr(attr xml.Attr) error {
	v, err := hexBinaryValue(attr.Value, 64)
	if err != nil {
		return err
	}
	*h = HexBinary64(v)
	return nil
}

// DERControlType is the sep.xsd DERControlType bitmap, an extension of
// HexBinary32 (sep.xsd:3952). It carries the DER mode bits advertised by
// DERCapability.modesSupported and selected by DERSettings.modesEnabled.
type DERControlType = HexBinary32

// DeviceCategoryType is the sep.xsd DeviceCategoryType bitmap, an extension
// of HexBinary32 (sep.xsd:5830).
type DeviceCategoryType = HexBinary32
