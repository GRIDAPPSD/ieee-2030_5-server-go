package sep2

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
)

// ActivePower represents real power in watts. Value is xs:short (XSD
// Int16, -32768..32767) per sep.xsd; it was previously Go int64, which
// permitted wire values outside the schema's legal range. This is a
// deliberate breaking API change: no compat shim. Downstream callers that
// need a wider intermediate type adapt at their own boundary.
type ActivePower struct {
	Multiplier int8  `xml:"multiplier"`
	Value      int16 `xml:"value"`
}

// ReactivePower represents reactive power in volt-amperes reactive. Value
// is xs:short (XSD Int16, -32768..32767) per sep.xsd; see ActivePower's
// doc comment for the rationale on the int64 to int16 narrowing.
type ReactivePower struct {
	Multiplier int8  `xml:"multiplier"`
	Value      int16 `xml:"value"`
}

// ApparentPower represents apparent power in volt-amperes. Unlike
// ActivePower, sep.xsd types its value UInt16.
type ApparentPower struct {
	Multiplier int8   `xml:"multiplier"`
	Value      uint16 `xml:"value"`
}

// VoltageRMS represents an rms voltage in volts. sep.xsd types its value
// UInt16.
type VoltageRMS struct {
	Multiplier int8   `xml:"multiplier"`
	Value      uint16 `xml:"value"`
}

// PerCent is a percentage in hundredths of a percent (10000 = 100%).
// IEEE 2030.5-2018 Annex B.2.3.4 "Types package", "PerCent object
// (UInt16)": range 0 to 10000. Unlike ActivePower/ReactivePower, PerCent
// is a schema simple type: it marshals as bare element text, never as
// multiplier+value children.
type PerCent uint16

// SignedPerCent is a signed percentage in hundredths of a percent (10000
// = 100%, -10000 = -100%). IEEE 2030.5-2018 Annex B.2.3.4 "Types
// package", "SignedPerCent object (Int16)": range -10000 to 10000.
type SignedPerCent int16

const (
	maxPerCent       = 10000
	minSignedPerCent = -10000
	maxSignedPerCent = 10000
)

// UnmarshalXML refuses anything but bare integer element text in range.
// Without this, encoding/xml's default scalar decode skips unrecognized
// child elements (the pre-fix ActivePower multiplier+value shape) and
// silently yields a zero value with a nil error.
func (p *PerCent) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var text string
	if err := d.DecodeElement(&text, &start); err != nil {
		return fmt.Errorf("sep2: PerCent: %w", err)
	}
	v, err := parsePerCent(text)
	if err != nil {
		return err
	}
	*p = v
	return nil
}

func parsePerCent(text string) (PerCent, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, fmt.Errorf("sep2: PerCent: empty or non-scalar element")
	}
	n, err := strconv.ParseUint(trimmed, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("sep2: PerCent: %q: %w", trimmed, err)
	}
	if n > maxPerCent {
		return 0, fmt.Errorf("sep2: PerCent %d out of range [0, %d]", n, maxPerCent)
	}
	return PerCent(n), nil
}

// MarshalXML rejects a value outside PerCent's XSD range rather than
// serializing an illegal wire value, mirroring RealEnergy's guard.
func (p PerCent) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if p > maxPerCent {
		return fmt.Errorf("sep2: PerCent %d out of range [0, %d]", uint16(p), maxPerCent)
	}
	return e.EncodeElement(uint16(p), start)
}

// UnmarshalJSON applies the same range refusal as UnmarshalXML; server-go
// ingests JSON fixtures for this package's types.
func (p *PerCent) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("sep2: PerCent: %w", err)
	}
	if n < 0 || n > maxPerCent {
		return fmt.Errorf("sep2: PerCent %d out of range [0, %d]", n, maxPerCent)
	}
	*p = PerCent(n)
	return nil
}

// MarshalJSON mirrors MarshalXML's range guard.
func (p PerCent) MarshalJSON() ([]byte, error) {
	if p > maxPerCent {
		return nil, fmt.Errorf("sep2: PerCent %d out of range [0, %d]", uint16(p), maxPerCent)
	}
	return json.Marshal(uint16(p))
}

// UnmarshalXML mirrors PerCent.UnmarshalXML for the signed range.
func (s *SignedPerCent) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var text string
	if err := d.DecodeElement(&text, &start); err != nil {
		return fmt.Errorf("sep2: SignedPerCent: %w", err)
	}
	v, err := parseSignedPerCent(text)
	if err != nil {
		return err
	}
	*s = v
	return nil
}

func parseSignedPerCent(text string) (SignedPerCent, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, fmt.Errorf("sep2: SignedPerCent: empty or non-scalar element")
	}
	n, err := strconv.ParseInt(trimmed, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("sep2: SignedPerCent: %q: %w", trimmed, err)
	}
	if n < minSignedPerCent || n > maxSignedPerCent {
		return 0, fmt.Errorf("sep2: SignedPerCent %d out of range [%d, %d]", n, minSignedPerCent, maxSignedPerCent)
	}
	return SignedPerCent(n), nil
}

// MarshalXML mirrors PerCent.MarshalXML for the signed range.
func (s SignedPerCent) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if s < minSignedPerCent || s > maxSignedPerCent {
		return fmt.Errorf("sep2: SignedPerCent %d out of range [%d, %d]", int16(s), minSignedPerCent, maxSignedPerCent)
	}
	return e.EncodeElement(int16(s), start)
}

// UnmarshalJSON mirrors PerCent.UnmarshalJSON for the signed range.
func (s *SignedPerCent) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err != nil {
		return fmt.Errorf("sep2: SignedPerCent: %w", err)
	}
	if n < minSignedPerCent || n > maxSignedPerCent {
		return fmt.Errorf("sep2: SignedPerCent %d out of range [%d, %d]", n, minSignedPerCent, maxSignedPerCent)
	}
	*s = SignedPerCent(n)
	return nil
}

// MarshalJSON mirrors MarshalXML's range guard.
func (s SignedPerCent) MarshalJSON() ([]byte, error) {
	if s < minSignedPerCent || s > maxSignedPerCent {
		return nil, fmt.Errorf("sep2: SignedPerCent %d out of range [%d, %d]", int16(s), minSignedPerCent, maxSignedPerCent)
	}
	return json.Marshal(int16(s))
}

// FixedPowerFactor for fixed power factor control modes.
type FixedPowerFactor struct {
	Displacement uint16 `xml:"displacement"`
	Excitation   bool   `xml:"excitation"`
	Multiplier   int8   `xml:"multiplier"`
}

// PowerFactor represents a power factor value.
type PowerFactor struct {
	Displacement uint16 `xml:"displacement"`
	Multiplier   int8   `xml:"multiplier"`
}
