// Package derstatus decodes a DERStatus the way the admin plane shows it, so
// the Devices payload and the DERMS roll-up read connection, inverter state
// and freshness through one definition.
package derstatus

import "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"

// DefaultPollRateSeconds is the DERList pollRate the server serves, and the
// rate a stale judgement assumes when none is given.
const DefaultPollRateSeconds uint32 = 900

// Names of the DERStatus element a Connect was read from.
const (
	SourceConnectStatus     = "connectStatus"
	SourceGenConnectStatus  = "genConnectStatus"
	SourceStorConnectStatus = "storConnectStatus"
)

const (
	bitConnected = 1 << 0
	bitEnergized = 1 << 1 // connectStatus (2023) only
	bitAvailable = 1 << 1 // 2018 ConnectStatusType
	bitOperating = 1 << 2
	bitTest      = 1 << 3
	bitFault     = 1 << 4

	reserved2018 = 0xE0
	reserved2023 = 0xFC
)

// Connect is a DER's own connection status as it reported it. Bit 0 is
// always decoded: a clear bit 0 is a positive report of disconnection. Raw is
// kept so a UI can show the unmodified value.
type Connect struct {
	Source string `json:"source"`
	Raw    uint8  `json:"raw"`
	// Since is the element's dateTime: when the state applied, not when it
	// was last refreshed.
	Since     int64 `json:"since"`
	Connected bool  `json:"connected"`
	// Energized is set only for connectStatus (2023); 2018 has no
	// counterpart.
	Energized *bool `json:"energized,omitempty"`
	// Available, Operating, Test and Fault are 2018 bits and stay false for
	// connectStatus, whose other bits are reserved.
	Available    bool  `json:"available"`
	Operating    bool  `json:"operating"`
	Test         bool  `json:"test"`
	Fault        bool  `json:"fault"`
	ReservedBits uint8 `json:"reservedBits"`
	// Also is the storConnectStatus of a DER that reports it beside
	// genConnectStatus (a PV plus battery unit): the 2018 fields are
	// separate, so neither stands for the other.
	Also *Connect `json:"also,omitempty"`
}

// AnyConnected reports whether the DER says it is connected in any field it
// reported. Counting a PV-plus-battery unit as disconnected because one half
// is would hide a connected, exporting battery.
func (c *Connect) AnyConnected() bool {
	return c.Connected || (c.Also != nil && c.Also.Connected)
}

// ConnectOf reads the connection status: connectStatus (2023) when present,
// since it supersedes both 2018 fields; otherwise genConnectStatus, with
// storConnectStatus in Also when both are reported; otherwise
// storConnectStatus. It returns nil when the status carries none.
func ConnectOf(s sep2.DERStatus) *Connect {
	switch {
	case s.ConnectStatus != nil:
		raw := uint8(s.ConnectStatus.Value)
		energized := raw&bitEnergized != 0
		return &Connect{
			Source: SourceConnectStatus, Raw: raw, Since: s.ConnectStatus.DateTime,
			Connected: raw&bitConnected != 0, Energized: &energized,
			ReservedBits: raw & reserved2023,
		}
	case s.GenConnectStatus != nil:
		c := connect2018(SourceGenConnectStatus, *s.GenConnectStatus)
		if s.StorConnectStatus != nil {
			c.Also = connect2018(SourceStorConnectStatus, *s.StorConnectStatus)
		}
		return c
	case s.StorConnectStatus != nil:
		return connect2018(SourceStorConnectStatus, *s.StorConnectStatus)
	}
	return nil
}

func connect2018(source string, c sep2.ConnectStatusType) *Connect {
	raw := uint8(c.Value)
	return &Connect{
		Source: source, Raw: raw, Since: c.DateTime,
		Connected: raw&bitConnected != 0, Available: raw&bitAvailable != 0,
		Operating: raw&bitOperating != 0, Test: raw&bitTest != 0, Fault: raw&bitFault != 0,
		ReservedBits: raw & reserved2018,
	}
}

// Inverter is the reported InverterStatusType code, one value and not a
// bitmap. Labels belong to the UI.
type Inverter struct {
	Code  uint8 `json:"code"`
	Since int64 `json:"since"`
}

// InverterOf returns nil when the status has no inverterStatus.
func InverterOf(s sep2.DERStatus) *Inverter {
	if s.InverterStatus == nil {
		return nil
	}
	return &Inverter{Code: s.InverterStatus.Value, Since: s.InverterStatus.DateTime}
}

// Freshness is how current a DERStatus is, judged from readingTime, which is
// a client clock.
type Freshness struct {
	ReadingTime int64 `json:"readingTime"`
	// AgeSeconds is 0 when there is no reading time or it is in the future.
	AgeSeconds    int64 `json:"ageSeconds"`
	Stale         bool  `json:"stale"`
	NoReadingTime bool  `json:"noReadingTime"`
	ClockAhead    bool  `json:"clockAhead"`
}

// StaleAfterSeconds is how old a reading may be before it is stale: two
// missed posts plus a minute of slack. A zero pollRate takes the default.
func StaleAfterSeconds(pollRate uint32) int64 {
	if pollRate == 0 {
		pollRate = DefaultPollRateSeconds
	}
	return 2*int64(pollRate) + 60
}

// Assess judges readingTime against now (both Unix seconds). A reading with
// no time cannot be aged and counts as stale. A reading dated more than one
// pollRate ahead of now is flagged ClockAhead and is also stale: it cannot be
// aged either, and must never read as fresh or connected. Skew inside one
// pollRate is treated as current.
func Assess(readingTime, now int64, pollRate uint32) Freshness {
	if pollRate == 0 {
		pollRate = DefaultPollRateSeconds
	}
	f := Freshness{ReadingTime: readingTime}
	if readingTime == 0 {
		f.NoReadingTime = true
		f.Stale = true
		return f
	}
	age := now - readingTime
	if age < 0 {
		f.ClockAhead = -age > int64(pollRate)
		f.Stale = f.ClockAhead
		return f
	}
	f.AgeSeconds = age
	f.Stale = age > StaleAfterSeconds(pollRate)
	return f
}

// Decoded is everything the admin plane shows from one DERStatus.
type Decoded struct {
	Connect  *Connect  `json:"connect"`
	Inverter *Inverter `json:"inverter"`
	Freshness
}

// Decode reads connection, inverter state and freshness from s.
func Decode(s sep2.DERStatus, now int64, pollRate uint32) Decoded {
	return Decoded{
		Connect:   ConnectOf(s),
		Inverter:  InverterOf(s),
		Freshness: Assess(s.ReadingTime, now, pollRate),
	}
}
