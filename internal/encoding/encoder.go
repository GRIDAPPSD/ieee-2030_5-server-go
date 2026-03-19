package encoding

// Encoder abstracts XML/EXI encoding for IEEE 2030.5 payloads.
// Phase 1 implements XML only; EXI will be added in Phase 3.
type Encoder interface {
	ContentType() string
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

const (
	ContentTypeSEPXML = "application/sep+xml"
	ContentTypeSEPEXI = "application/sep-exi"
)

// NegotiateEncoder selects the appropriate encoder based on the Accept header.
// Phase 1: always returns XML. Phase 3 will add EXI support.
func NegotiateEncoder(accept string) Encoder {
	return NewXMLEncoder()
}
