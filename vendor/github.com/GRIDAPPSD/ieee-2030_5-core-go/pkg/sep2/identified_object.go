package sep2

// IdentifiedObject holds the mRID, description and version elements that open
// the xsd:sequence of every type extending IdentifiedObject, directly or
// through its Subscribable and Respondable variants (sep.xsd:5324). It is
// embedded so the three elements marshal flat, after the extending type's
// Resource attributes and ahead of its own elements.
//
// mRID is minOccurs=1, so it has no omitempty and an unset value serializes as
// an empty element. Version is a pointer so an explicit 0 stays distinct from
// unset.
type IdentifiedObject struct {
	MRID        string  `xml:"mRID"`
	Description string  `xml:"description,omitempty"`
	Version     *uint16 `xml:"version,omitempty"`
}

// Copy returns an independent copy.
func (o IdentifiedObject) Copy() IdentifiedObject {
	c := o
	c.Version = clonePtr(o.Version)
	return c
}
