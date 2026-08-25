package encoding

import "encoding/xml"

type xmlEncoder struct{}

// NewXMLEncoder returns an Encoder that marshals/unmarshals IEEE 2030.5
// resources as XML with the urn:ieee:std:2030.5:ns namespace.
func NewXMLEncoder() Encoder {
	return &xmlEncoder{}
}

func (e *xmlEncoder) ContentType() string {
	return ContentTypeSEPXML
}

func (e *xmlEncoder) Marshal(v any) ([]byte, error) {
	return xml.Marshal(v)
}

func (e *xmlEncoder) Unmarshal(data []byte, v any) error {
	return xml.Unmarshal(data, v)
}
