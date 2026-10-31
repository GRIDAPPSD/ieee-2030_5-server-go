package sep2

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"math"
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

// CurrentRMS is an rms current in amperes (uom 5). sep.xsd types its value
// UInt16.
type CurrentRMS struct {
	Multiplier int8   `xml:"multiplier"`
	Value      uint16 `xml:"value"`
}

// AmpereHour is available electric charge in ampere-hours (uom 106). sep.xsd
// types its value UInt16.
type AmpereHour struct {
	Multiplier int8   `xml:"multiplier"`
	Value      uint16 `xml:"value"`
}

// WattHour is active energy in watt-hours (uom 72). sep.xsd types its value
// UInt16.
type WattHour struct {
	Multiplier int8   `xml:"multiplier"`
	Value      uint16 `xml:"value"`
}

// ReactiveSusceptance is reactive susceptance in siemens (uom 53). sep.xsd
// types its value UInt16.
type ReactiveSusceptance struct {
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

// checkBounded is the semantic range check for the bounded integer simple
// types (PerCent, SignedPerCent, OneHourRange); rangeError owns the message.
func checkBounded(name string, n, lo, hi int64) error {
	if n < lo || n > hi {
		return rangeError(name, n, lo, hi)
	}
	return nil
}

func rangeError(name string, n, lo, hi int64) error {
	return fmt.Errorf("sep2: %s %d out of range [%d, %d]", name, n, lo, hi)
}

// narrowInt16 and narrowUint16 are the only places a parsed value is
// converted to a 16-bit type. Each checks the semantic range, then guards
// against the target type's own limits immediately before the conversion.
func narrowInt16(name string, n, lo, hi int64) (int16, error) {
	if err := checkBounded(name, n, lo, hi); err != nil {
		return 0, err
	}
	if n < math.MinInt16 || n > math.MaxInt16 {
		return 0, rangeError(name, n, lo, hi)
	}
	return int16(n), nil
}

func narrowUint16(name string, n, lo, hi int64) (uint16, error) {
	if err := checkBounded(name, n, lo, hi); err != nil {
		return 0, err
	}
	if n < 0 || n > math.MaxUint16 {
		return 0, rangeError(name, n, lo, hi)
	}
	return uint16(n), nil
}

// narrowFunc is narrowInt16 or narrowUint16.
type narrowFunc[T int16 | uint16] func(name string, n, lo, hi int64) (T, error)

// parseBounded trims element text, parses it as a decimal integer and
// narrows it with the supplied narrowing function.
func parseBounded[T int16 | uint16](name, text string, lo, hi int64, narrow narrowFunc[T]) (T, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, fmt.Errorf("sep2: %s: empty or non-scalar element", name)
	}
	n, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("sep2: %s: %q: %w", name, trimmed, err)
	}
	return narrow(name, n, lo, hi)
}

// decodeBoundedXML reads element text only. Without that, encoding/xml's
// default scalar decode skips unrecognized child elements (the pre-fix
// ActivePower multiplier+value shape) and silently yields a zero value with
// a nil error.
func decodeBoundedXML[T int16 | uint16](d *xml.Decoder, start xml.StartElement, name string, lo, hi int64, narrow narrowFunc[T]) (T, error) {
	var text string
	if err := d.DecodeElement(&text, &start); err != nil {
		return 0, fmt.Errorf("sep2: %s: %w", name, err)
	}
	return parseBounded(name, text, lo, hi, narrow)
}

// decodeBoundedJSON applies the same range refusal as decodeBoundedXML;
// server-go ingests JSON fixtures for this package's types.
func decodeBoundedJSON[T int16 | uint16](data []byte, name string, lo, hi int64, narrow narrowFunc[T]) (T, error) {
	var n int64
	if err := json.Unmarshal(data, &n); err != nil {
		return 0, fmt.Errorf("sep2: %s: %w", name, err)
	}
	return narrow(name, n, lo, hi)
}

// UnmarshalXML refuses anything but bare integer element text in range.
func (p *PerCent) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	v, err := decodeBoundedXML(d, start, "PerCent", 0, maxPerCent, narrowUint16)
	if err != nil {
		return err
	}
	*p = PerCent(v)
	return nil
}

// MarshalXML rejects a value outside PerCent's XSD range rather than
// serializing an illegal wire value, mirroring RealEnergy's guard.
func (p PerCent) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if err := checkBounded("PerCent", int64(p), 0, maxPerCent); err != nil {
		return err
	}
	return e.EncodeElement(uint16(p), start)
}

// UnmarshalJSON applies the same range refusal as UnmarshalXML.
func (p *PerCent) UnmarshalJSON(data []byte) error {
	v, err := decodeBoundedJSON(data, "PerCent", 0, maxPerCent, narrowUint16)
	if err != nil {
		return err
	}
	*p = PerCent(v)
	return nil
}

// MarshalJSON mirrors MarshalXML's range guard.
func (p PerCent) MarshalJSON() ([]byte, error) {
	if err := checkBounded("PerCent", int64(p), 0, maxPerCent); err != nil {
		return nil, err
	}
	return json.Marshal(uint16(p))
}

// parseSignedPerCent is kept for callers elsewhere in the package.
func parseSignedPerCent(text string) (SignedPerCent, error) {
	v, err := parseBounded("SignedPerCent", text, minSignedPerCent, maxSignedPerCent, narrowInt16)
	return SignedPerCent(v), err
}

// UnmarshalXML mirrors PerCent.UnmarshalXML for the signed range.
func (s *SignedPerCent) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	v, err := decodeBoundedXML(d, start, "SignedPerCent", minSignedPerCent, maxSignedPerCent, narrowInt16)
	if err != nil {
		return err
	}
	*s = SignedPerCent(v)
	return nil
}

// MarshalXML mirrors PerCent.MarshalXML for the signed range.
func (s SignedPerCent) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if err := checkBounded("SignedPerCent", int64(s), minSignedPerCent, maxSignedPerCent); err != nil {
		return err
	}
	return e.EncodeElement(int16(s), start)
}

// UnmarshalJSON mirrors PerCent.UnmarshalJSON for the signed range.
func (s *SignedPerCent) UnmarshalJSON(data []byte) error {
	v, err := decodeBoundedJSON(data, "SignedPerCent", minSignedPerCent, maxSignedPerCent, narrowInt16)
	if err != nil {
		return err
	}
	*s = SignedPerCent(v)
	return nil
}

// MarshalJSON mirrors MarshalXML's range guard.
func (s SignedPerCent) MarshalJSON() ([]byte, error) {
	if err := checkBounded("SignedPerCent", int64(s), minSignedPerCent, maxSignedPerCent); err != nil {
		return nil, err
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
