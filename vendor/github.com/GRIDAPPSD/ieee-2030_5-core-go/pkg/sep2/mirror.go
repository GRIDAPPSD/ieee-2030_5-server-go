package sep2

import "encoding/xml"

// RoleFlagsValue is the sep.xsd RoleFlagsType bitmap, an extension of
// HexBinary16 (sep.xsd:6031-6045), describing the roles that apply to a
// usage point (bit 0 isMirror, bit 1 isPremisesAggregationPoint, bit 2
// isPEV, bit 3 isDER, bit 4 isRevenueQuality, bit 5 isDC, bit 6 isSubmeter,
// bits 7-15 reserved).
//
// It is an alias for HexBinary16 rather than a distinct type so that the
// hexBinary encoding lives in exactly one place (hexbinary.go) for the whole
// family. Every hexBinary-derived field in this package had the same latent
// defect that RoleFlagsType had, so the fix belongs to the family, not to
// this one field.
type RoleFlagsValue = HexBinary16

// MirrorUsagePoint is a client-created resource for reporting metering data.
// Inverters POST to /mup to register, then POST readings to /mup/{id}/mr.
// Spec reference: section 10.11
//
// Field order matches the sep.xsd sequence, which is load-bearing for a
// schema-validating client: MirrorUsagePoint extends UsagePointBase, whose
// own sequence is roleFlags, serviceCategoryKind, status (sep.xsd:6577
// -6588), preceded by the IdentifiedObject members mRID, description,
// version (sep.xsd:5324-5349) that Resource/IdentifiedObject contribute,
// then the MirrorUsagePoint extension's own deviceLFDI,
// MirrorMeterReading, postRate (sep.xsd:6472-6493). encoding/xml emits
// struct fields in declaration order, so this order is not cosmetic.
//
// There is no MirrorMeterReadingListLink child in sep.xsd for this type:
// MirrorUsagePoint carries MirrorMeterReading directly (inline, repeated),
// not a link to a list (sep.xsd:6472-6493, confirmed absent from the
// schema's full element/type index). A prior version of this struct
// emitted such a link; it has been removed rather than replaced, because
// the schema defines no link-typed equivalent to redirect it to.
type MirrorUsagePoint struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MirrorUsagePoint"`
	Resource
	MRID        string `xml:"mRID,omitempty"`
	Description string `xml:"description,omitempty"`
	// roleFlags is required (min=1) in sep.xsd's UsagePointBase sequence
	// (sep.xsd:6577-6588); "omitempty" would drop the element entirely when
	// RoleFlags is the legitimate value 0 (no roles set), serving a document
	// missing a required element. It must always serialize regardless of
	// numeric value, same as ServiceCategoryKind and Status below.
	RoleFlags           RoleFlagsValue       `xml:"roleFlags"`
	ServiceCategoryKind uint8                `xml:"serviceCategoryKind"`
	Status              uint8                `xml:"status"`
	DeviceLFDI          string               `xml:"deviceLFDI,omitempty"`
	MirrorMeterReading  []MirrorMeterReading `xml:"MirrorMeterReading,omitempty"`
	PostRate            *uint32              `xml:"postRate,omitempty"`
}

// Copy returns an independent copy.
func (m MirrorUsagePoint) Copy() MirrorUsagePoint {
	c := m
	if m.PostRate != nil {
		v := *m.PostRate
		c.PostRate = &v
	}
	if m.MirrorMeterReading != nil {
		c.MirrorMeterReading = make([]MirrorMeterReading, len(m.MirrorMeterReading))
		for i, r := range m.MirrorMeterReading {
			c.MirrorMeterReading[i] = r.Copy()
		}
	}
	return c
}

// MirrorUsagePointList is a list of MirrorUsagePoint resources.
type MirrorUsagePointList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MirrorUsagePointList"`
	ListResource
	MirrorUsagePoint []MirrorUsagePoint `xml:"MirrorUsagePoint,omitempty"`
}

// MirrorMeterReading contains metering data posted by a device.
//
// Field order matches the canonical sep.xsd sequence for
// MirrorMeterReading -> MeterReadingBase -> IdentifiedObject -> Resource
// (sep.xsd:6416, :6452, :5324): mRID, description, version,
// lastUpdateTime, MirrorReadingSet, nextUpdateTime, Reading, ReadingType.
// This struct implements mRID (position 1), description (position 2),
// lastUpdateTime (position 4), Reading (position 7), and ReadingType
// (position 8); Reading is declared before ReadingType below to match.
// Prior code emitted ReadingType before Reading, which was latent while
// the only client behavior was posting ReadingType alone, but breaks a
// strict sequence-validating parser the moment both are present.
//
// MRID has no omitempty: it is minOccurs=1 via IdentifiedObject
// (sep.xsd:5324), so a zero-value mRID must still serialize; omitempty
// would silently drop a required element.
type MirrorMeterReading struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MirrorMeterReading"`
	Resource
	MRID           string       `xml:"mRID"`
	Description    string       `xml:"description,omitempty"`
	LastUpdateTime int64        `xml:"lastUpdateTime,omitempty"`
	Reading        *Reading     `xml:"Reading,omitempty"`
	ReadingType    *ReadingType `xml:"ReadingType,omitempty"`
}

// Copy returns an independent copy.
func (m MirrorMeterReading) Copy() MirrorMeterReading {
	c := m
	if m.ReadingType != nil {
		rt := m.ReadingType.Copy()
		c.ReadingType = &rt
	}
	if m.Reading != nil {
		r := m.Reading.Copy()
		c.Reading = &r
	}
	return c
}

// MirrorMeterReadingList is a list of MirrorMeterReading resources.
type MirrorMeterReadingList struct {
	XMLName xml.Name `xml:"urn:ieee:std:2030.5:ns MirrorMeterReadingList"`
	ListResource
	MirrorMeterReading []MirrorMeterReading `xml:"MirrorMeterReading,omitempty"`
}
