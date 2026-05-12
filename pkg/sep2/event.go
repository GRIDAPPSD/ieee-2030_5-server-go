package sep2

// EventStatus describes the current state of an event.
type EventStatus struct {
	CurrentStatus            uint8  `xml:"currentStatus"`
	DateTime                 int64  `xml:"dateTime"`
	PotentiallySuperseded    bool   `xml:"potentiallySuperseded"`
	PotentiallySupersededTime *int64 `xml:"potentiallySupersededTime,omitempty"`
	Reason                   string `xml:"reason,omitempty"`
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
// Spec reference: IEEE 2030.5 §10.1.3 (Event rules). Event extends the XSD
// `RespondableResource` base, which contributes the `replyTo` and
// `responseRequired` child elements. Element order matches the 2023 XSD:
// `replyTo`, `responseRequired`, then identified-object fields (`mRID`,
// `description`), then `EventStatus`, `interval`. Field declaration order
// in this struct controls `encoding/xml` marshal order — do not reorder
// without verifying the XSD.
type Event struct {
	SubscribableResource

	// ReplyTo is the URI a client POSTs its `Response` resource to when
	// acknowledging this event. Relative URIs resolve against the
	// server's base URL; absolute URIs are used verbatim. Spec: §10.1.3
	// and 2023 XSD `RespondableResource.replyTo` (HRef).
	ReplyTo string `xml:"replyTo,omitempty"`

	// ResponseRequired is a HexBinary8 bitmap selecting which transition
	// statuses require a Response POST from the client. Bits per
	// IEEE 2030.5 Table 32: bit 0 = message received, bit 1 = specific
	// response, bit 2 = response on transition. Spec: §10.1.3 and 2023
	// XSD `RespondableResource.responseRequired` (HexBinary8).
	ResponseRequired *uint8 `xml:"responseRequired,omitempty"`

	MRID        string            `xml:"mRID,omitempty"`
	Description string            `xml:"description,omitempty"`
	EventStatus *EventStatus      `xml:"EventStatus,omitempty"`
	Interval    *DateTimeInterval `xml:"interval,omitempty"`
}

// RandomizableEvent extends Event with randomization parameters.
type RandomizableEvent struct {
	Event
	RandomizeDuration *int32 `xml:"randomizeDuration,omitempty"`
	RandomizeStart    *int32 `xml:"randomizeStart,omitempty"`
}

// EventStatus current status values per spec.
const (
	EventStatusScheduled   uint8 = 0
	EventStatusActive      uint8 = 1
	EventStatusCancelled   uint8 = 2
	EventStatusSuperseded  uint8 = 4
	EventStatusComplete    uint8 = 5
)
