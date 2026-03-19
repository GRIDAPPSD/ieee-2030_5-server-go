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
type Event struct {
	SubscribableResource
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
