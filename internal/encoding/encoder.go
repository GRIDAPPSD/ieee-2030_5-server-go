package encoding

// Encoder abstracts encoding for IEEE 2030.5 payloads.
// XML is the only supported encoding (EXI was removed in IEEE-002).
type Encoder interface {
	ContentType() string
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

// ContentTypeSEPXML is the MIME type for IEEE 2030.5 XML payloads.
const ContentTypeSEPXML = "application/sep+xml"

// NegotiateEncoder returns the XML encoder for any Accept header value.
// IEEE 2030.5 XML is the only supported encoding on this server.
func NegotiateEncoder(_ string) Encoder {
	return NewXMLEncoder()
}
