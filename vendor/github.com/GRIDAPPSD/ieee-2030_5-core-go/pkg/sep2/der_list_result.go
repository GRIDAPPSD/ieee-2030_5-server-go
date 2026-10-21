package sep2

import (
	"encoding/xml"
	"fmt"
	"io"
)

// RejectedDERControl is a DERControl that failed to decode only because of
// its DERControlBase, whether a range rule such as a FixedVar refType outside
// 1 to 3 or a parse error. The event identity is kept so a client can answer
// it with Response 253 (IEEE 2030.5-2018 Table 27) at its replyTo. MRID and
// ReplyTo are optional on the wire, so either can be empty.
type RejectedDERControl struct {
	RandomizableEvent
	// Err is the item's DERControlBase decode error, wrapped with the item's
	// index and mRID.
	Err error
}

// DERControlListResult decodes a DERControlList one DERControl at a time.
// An item whose DERControlBase fails to decode goes to Rejected and never to
// DERControl, so a consumer cannot apply it by accident. An item whose event
// fields fail still fails the whole decode: it carries no trustworthy
// replyTo to respond to. The same mRID can appear in both DERControl and
// Rejected, and the caller decides which wins. DERControlList itself stays
// strict.
type DERControlListResult struct {
	DERControlList
	Rejected []RejectedDERControl `xml:"-"`
}

// UnmarshalXML decodes the list's own fields through DERControlList and each
// DERControl through DERControl, so every value check stays with the type
// decoders that own it.
func (r *DERControlListResult) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	shell := []xml.Token{resolvedStart(start)}
	var controls []DERControl
	var rejected []RejectedDERControl
	for {
		tok, err := d.Token()
		if err != nil {
			return fmt.Errorf("sep2: DERControlList: %w", err)
		}
		if end, ok := tok.(xml.EndElement); ok {
			shell = append(shell, end)
			break
		}
		child, ok := tok.(xml.StartElement)
		if !ok {
			shell = append(shell, xml.CopyToken(tok))
			continue
		}
		item, err := copyElement(d, child)
		if err != nil {
			if child.Name.Local == "DERControl" {
				return fmt.Errorf("sep2: DERControl %d: %w", len(controls)+len(rejected), err)
			}
			return fmt.Errorf("sep2: DERControlList: %w", err)
		}
		// Matches DERControlList's untagged-namespace field, so the same
		// children reach DERControl decoding as on the strict path.
		if child.Name.Local != "DERControl" {
			shell = append(shell, item...)
			continue
		}
		n := len(controls) + len(rejected)
		var c DERControl
		baseErr := decodeTokens(item, &c)
		if baseErr == nil {
			controls = append(controls, c)
			continue
		}
		// Decoding again without DERControlBase separates a bad base from bad
		// event fields while reusing DERControl's own event decoding.
		var ident DERControl
		if err := decodeTokens(withoutChild(item, "DERControlBase"), &ident); err != nil {
			return fmt.Errorf("sep2: DERControl %d: %w", n, err)
		}
		rejected = append(rejected, RejectedDERControl{
			RandomizableEvent: ident.RandomizableEvent,
			Err:               fmt.Errorf("sep2: DERControl %d (mRID %q): %w", n, ident.MRID, baseErr),
		})
	}

	var list DERControlList
	if err := decodeTokens(shell, &list); err != nil {
		return fmt.Errorf("sep2: DERControlList: %w", err)
	}
	list.DERControl = controls
	*r = DERControlListResult{DERControlList: list, Rejected: rejected}
	return nil
}

// copyElement returns start and every token through its matching end
// element. Tokens are copied because the decoder reuses their buffers.
func copyElement(d *xml.Decoder, start xml.StartElement) ([]xml.Token, error) {
	toks := []xml.Token{resolvedStart(start)}
	for depth := 1; depth > 0; {
		tok, err := d.Token()
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			toks = append(toks, resolvedStart(t))
			continue
		case xml.EndElement:
			depth--
		}
		toks = append(toks, xml.CopyToken(tok))
	}
	return toks, nil
}

// withoutChild drops each direct child element of toks[0] named local.
func withoutChild(toks []xml.Token, local string) []xml.Token {
	out := make([]xml.Token, 0, len(toks))
	depth, skip := 0, 0
	for _, tok := range toks {
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			if skip == 0 && depth == 2 && t.Name.Local == local {
				skip = depth
			}
		case xml.EndElement:
			if skip != 0 && depth == skip {
				skip = 0
				depth--
				continue
			}
			depth--
		}
		if skip == 0 {
			out = append(out, tok)
		}
	}
	return out
}

// resolvedStart copies start without its namespace declarations. The decoder
// has already resolved every name in it, and replaying a declaration would
// translate those names a second time: xmlns="s" xmlns:s="<ns>" resolves an
// unprefixed name to "s", and a replay would turn "s" into <ns>, accepting a
// document the strict decode refuses.
func resolvedStart(start xml.StartElement) xml.StartElement {
	out := xml.StartElement{Name: start.Name, Attr: make([]xml.Attr, 0, len(start.Attr))}
	for _, a := range start.Attr {
		if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
			continue
		}
		out.Attr = append(out.Attr, a)
	}
	return out
}

// decodeTokens decodes tokens copied by copyElement or resolvedStart.
func decodeTokens(toks []xml.Token, v any) error {
	src := tokenSlice(toks)
	return xml.NewTokenDecoder(&src).Decode(v)
}

type tokenSlice []xml.Token

func (s *tokenSlice) Token() (xml.Token, error) {
	if len(*s) == 0 {
		return nil, io.EOF
	}
	tok := (*s)[0]
	*s = (*s)[1:]
	return tok, nil
}
