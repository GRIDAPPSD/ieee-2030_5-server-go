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
// Returns nil if the requested encoding is not supported (caller should return 406).
func NegotiateEncoder(accept string) Encoder {
	if accept == ContentTypeSEPEXI {
		return nil // EXI not yet supported
	}
	return NewXMLEncoder()
}

// IsEXIRequested returns true if the Accept header requests EXI encoding.
func IsEXIRequested(accept string) bool {
	return accept == ContentTypeSEPEXI
}
