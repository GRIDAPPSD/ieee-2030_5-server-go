package sep2

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// EventStatus describes the current state of an event.
type EventStatus struct {
	CurrentStatus             uint8  `xml:"currentStatus"`
	DateTime                  int64  `xml:"dateTime"`
	PotentiallySuperseded     bool   `xml:"potentiallySuperseded"`
	PotentiallySupersededTime *int64 `xml:"potentiallySupersededTime,omitempty"`
	Reason                    string `xml:"reason,omitempty"`
}

// Copy returns an independent copy.
func (e EventStatus) Copy() EventStatus {
	c := e
	if e.PotentiallySupersededTime != nil {
		v := *e.PotentiallySupersededTime
		c.PotentiallySupersededTime = &v
	}
	return c
}

// Event is the base type for schedulable events.
//
// Spec reference: IEEE 2030.5 section 10.1.3 (Event rules). Event extends
// the XSD `RespondableResource` base, which contributes `replyTo` and
// `responseRequired` as XML ATTRIBUTES, not child elements (sep.xsd:5435 and
// sep.xsd:5440 declare both with `<xs:attribute>`). Element order for the
// remaining child elements matches the XSD: identified-object fields (`mRID`,
// `description`), then `creationTime`, `EventStatus`, `interval`. Field
// declaration order in this struct controls `encoding/xml` marshal order for
// the ELEMENTS; attributes are emitted on the start tag regardless of
// position, so the two attribute fields stay declared first to mirror the
// XSD's derivation order for readers.
type Event struct {
	SubscribableResource

	// ReplyTo is the URI a client POSTs its `Response` resource to when
	// acknowledging this event. Relative URIs resolve against the
	// server's base URL; absolute URIs are used verbatim. Spec: section
	// 10.1.3 and XSD `RespondableResource.replyTo` (HRef).
	//
	// ATTRIBUTE, not element: sep.xsd:5435 declares it
	// `<xs:attribute name="replyTo" use="optional" type="xs:anyURI"/>`.
	// Emitting it as a child element made the EPRI reference client abort
	// the whole DERControlList parse.
	ReplyTo string `xml:"replyTo,attr,omitempty"`

	// ResponseRequired is a HexBinary8 bitmap selecting which transition
	// statuses require a Response POST from the client. Bits per
	// IEEE 2030.5 Table 32: bit 0 = message received, bit 1 = specific
	// response, bit 2 = response on transition. Spec: section 10.1.3 and
	// XSD `RespondableResource.responseRequired` (HexBinary8).
	//
	// ATTRIBUTE, not element: sep.xsd:5440 declares it
	// `<xs:attribute name="responseRequired" use="optional" default="00"
	// type="HexBinary8"/>`.
	//
	// The pointer is load-bearing and must stay a pointer. omitempty on an
	// attribute omits only the zero value of the field's own type, which for
	// a pointer is nil, so a non-nil pointer to 0x00 still reaches the wire
	// as responseRequired="00". That is what lets a server express
	// "explicitly no response wanted" distinguishably from "unset", which is
	// the default-versus-override semantic the server's response policy
	// depends on.
	ResponseRequired *HexBinary8 `xml:"responseRequired,attr,omitempty"`

	MRID        string `xml:"mRID,omitempty"`
	Description string `xml:"description,omitempty"`

	// CreationTime is the TimeType instant at which the server created
	// this event. It is a REQUIRED element: the 2023 XSD declares
	// `creationTime` minOccurs="1" maxOccurs="1" as the FIRST child
	// contributed by `Event` itself, before `EventStatus` and `interval`,
	// which is why it is declared here rather than appended (field order
	// drives `encoding/xml` marshal order).
	//
	// It is not decoration. A client uses creationTime to decide which of
	// two overlapping events of equal primacy supersedes the other: IEEE
	// 2030.5 defines a superseding event as a newer event from the same
	// program covering the same controls over an overlapping period, and
	// "newer" is creationTime. Concretely, the EPRI reference client's
	// block_supersede compares `x->creationTime > y->creationTime` when
	// primacies are equal, so with the element absent both events parse as
	// creationTime 0, neither can win, and the INCOMING event is the one
	// discarded. Omitting it therefore does not merely lose metadata: it
	// makes a server unable to replace a control it has already issued.
	//
	// No omitempty: zero is a legal-looking TimeType but a meaningless
	// creation instant, and the element is required, so a server that
	// leaves it unset should emit a visibly wrong `<creationTime>0</...>`
	// rather than a silently absent required element.
	CreationTime int64 `xml:"creationTime"`

	EventStatus *EventStatus      `xml:"EventStatus,omitempty"`
	Interval    *DateTimeInterval `xml:"interval,omitempty"`
}

// RandomizableEvent extends Event with randomization parameters.
type RandomizableEvent struct {
	Event
	RandomizeDuration *OneHourRange `xml:"randomizeDuration,omitempty"`
	RandomizeStart    *OneHourRange `xml:"randomizeStart,omitempty"`
}

// OneHourRange is IEEE 2030.5-2018 Annex B.2.3.4 "OneHourRangeType (Int16)":
// a count of seconds bounded to +/-3600, used by RandomizeDuration and
// RandomizeStart. sep.xsd restricts OneHourRangeType to [-3600, 3600], a
// tighter range than Int16 (xs:short, -32768..32767) alone would permit, so
// the type's own Marshal/Unmarshal enforce the schema's facet rather than
// relying on the field's storage width.
type OneHourRange int16

const (
	minOneHourRange = -3600
	maxOneHourRange = 3600
)

// UnmarshalXML refuses element text outside OneHourRangeType's range.
func (o *OneHourRange) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var text string
	if err := d.DecodeElement(&text, &start); err != nil {
		return fmt.Errorf("sep2: OneHourRange: %w", err)
	}
	v, err := parseOneHourRange(text)
	if err != nil {
		return err
	}
	*o = v
	return nil
}

func parseOneHourRange(text string) (OneHourRange, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, fmt.Errorf("sep2: OneHourRange: empty or non-scalar element")
	}
	n, err := strconv.ParseInt(trimmed, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("sep2: OneHourRange: %q: %w", trimmed, err)
	}
	if n < minOneHourRange || n > maxOneHourRange {
		return 0, fmt.Errorf("sep2: OneHourRange %d out of range [%d, %d]", n, minOneHourRange, maxOneHourRange)
	}
	return OneHourRange(n), nil
}

// MarshalXML rejects a value outside OneHourRangeType's range rather than
// serializing an illegal wire value, mirroring SignedPerCent's guard.
func (o OneHourRange) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if o < minOneHourRange || o > maxOneHourRange {
		return fmt.Errorf("sep2: OneHourRange %d out of range [%d, %d]", int16(o), minOneHourRange, maxOneHourRange)
	}
	return e.EncodeElement(int16(o), start)
}

// UnmarshalJSON applies the same range refusal as UnmarshalXML; server-go
// ingests JSON fixtures for this package's types.
func (o *OneHourRange) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("sep2: OneHourRange: %w", err)
	}
	if n < minOneHourRange || n > maxOneHourRange {
		return fmt.Errorf("sep2: OneHourRange %d out of range [%d, %d]", n, minOneHourRange, maxOneHourRange)
	}
	*o = OneHourRange(n)
	return nil
}

// MarshalJSON mirrors MarshalXML's range guard.
func (o OneHourRange) MarshalJSON() ([]byte, error) {
	if o < minOneHourRange || o > maxOneHourRange {
		return nil, fmt.Errorf("sep2: OneHourRange %d out of range [%d, %d]", int16(o), minOneHourRange, maxOneHourRange)
	}
	return json.Marshal(int16(o))
}

// EventStatus current status values per spec.
const (
	EventStatusScheduled  uint8 = 0
	EventStatusActive     uint8 = 1
	EventStatusCancelled  uint8 = 2
	EventStatusSuperseded uint8 = 4
	EventStatusComplete   uint8 = 5
)
