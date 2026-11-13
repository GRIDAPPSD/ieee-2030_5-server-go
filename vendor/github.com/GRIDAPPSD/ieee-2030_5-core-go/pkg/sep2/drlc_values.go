package sep2

import (
	"encoding/xml"
	"fmt"
	"math"
	"strings"
)

// The demand response value types below are children of EndDeviceControl in
// sep.xsd, in the schema's sequence order. DrResponse shares
// ApplianceLoadReduction, DutyCycle, Offset and SetPoint; in place of
// TargetReduction it carries AppliedTargetReduction, the same type.

// Highest defined values; sep.xsd and IEEE 2030.5-2018 reserve the rest.
const (
	maxApplianceLoadReductionType = 1  // 0 delay load, 1 temporary reduction
	maxUnitType                   = 12 // UnitType 0 kWh through 12 Unitless
	maxDutyCycle                  = 100
)

// Boolean is an xs:boolean: only true, false, 1 and 0 decode. An omitted
// element leaves the zero value, false.
type Boolean bool

// UnmarshalXML refuses an empty element and any spelling xs:boolean lacks,
// which encoding/xml's default bool decoding would read as false or accept
// (T, TRUE).
func (b *Boolean) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	text, err := simpleText(d)
	if err != nil {
		return fmt.Errorf("sep2: %s: %w", start.Name.Local, err)
	}
	switch strings.TrimSpace(text) {
	case "true", "1":
		*b = true
	case "false", "0":
		*b = false
	default:
		return fmt.Errorf("sep2: %s: %s is not an xs:boolean", start.Name.Local, quotePeerText([]byte(text)))
	}
	return nil
}

// ApplianceLoadReduction asks an ENERGY STAR appliance for a load reduction.
// Type is ApplianceLoadReductionType (UInt8).
type ApplianceLoadReduction struct {
	Type uint8 `xml:"type"`
}

// Copy returns an independent copy.
func (a ApplianceLoadReduction) Copy() ApplianceLoadReduction { return a }

// UnmarshalXML requires type, refuses a reserved ApplianceLoadReductionType,
// and refuses an unknown or repeated child.
func (a *ApplianceLoadReduction) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v ApplianceLoadReduction
	err := decodeChildren(d, start, "ApplianceLoadReduction", []string{"type"}, func(_, text string) error {
		n, err := parseUint(text, 8)
		if err == nil && n > maxApplianceLoadReductionType {
			err = fmt.Errorf("%d is reserved, want 0 or 1", n)
		}
		v.Type = uint8(n)
		return err
	})
	if err != nil {
		return err
	}
	*a = v
	return nil
}

// DutyCycle carries the maximum On-state duty cycle as a percentage of time.
type DutyCycle struct {
	NormalValue uint8 `xml:"normalValue"`
}

// Copy returns an independent copy.
func (d DutyCycle) Copy() DutyCycle { return d }

// UnmarshalXML requires normalValue and refuses a percentage above 100.
func (dc *DutyCycle) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v DutyCycle
	err := decodeChildren(d, start, "DutyCycle", []string{"normalValue"}, func(_, text string) error {
		n, err := parseUint(text, 8)
		if err == nil && n > maxDutyCycle {
			err = fmt.Errorf("%d is above 100 percent", n)
		}
		v.NormalValue = uint8(n)
		return err
	})
	if err != nil {
		return err
	}
	*dc = v
	return nil
}

// Offset is a requested change to the normal temperature set points
// (degrees C / 10) or load adjustment percentage. A nil field is absent.
type Offset struct {
	CoolingOffset                  *uint8   `xml:"coolingOffset,omitempty"`
	HeatingOffset                  *uint8   `xml:"heatingOffset,omitempty"`
	LoadAdjustmentPercentageOffset *PerCent `xml:"loadAdjustmentPercentageOffset,omitempty"`
}

// UnmarshalXML refuses an empty, repeated, unknown, foreign-namespace or
// nested child, and children out of schema order.
func (o *Offset) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v Offset
	err := decodeOptionalChildren(d, start, "Offset", []string{"coolingOffset", "heatingOffset", "loadAdjustmentPercentageOffset"}, func(name, text string) error {
		if name == "loadAdjustmentPercentageOffset" {
			n, err := parseBounded("PerCent", text, 0, maxPerCent, narrowUint16)
			p := PerCent(n)
			v.LoadAdjustmentPercentageOffset = &p
			return err
		}
		n, err := parseUint(text, 8)
		u := uint8(n)
		if name == "coolingOffset" {
			v.CoolingOffset = &u
		} else {
			v.HeatingOffset = &u
		}
		return err
	})
	if err != nil {
		return err
	}
	*o = v
	return nil
}

// Copy returns an independent copy.
func (o Offset) Copy() Offset {
	c := o
	c.CoolingOffset = clonePtr(o.CoolingOffset)
	c.HeatingOffset = clonePtr(o.HeatingOffset)
	c.LoadAdjustmentPercentageOffset = clonePtr(o.LoadAdjustmentPercentageOffset)
	return c
}

// SetPoint holds temperature set points in degrees C / 100. A nil field is
// absent.
type SetPoint struct {
	CoolingSetpoint *int16 `xml:"coolingSetpoint,omitempty"`
	HeatingSetpoint *int16 `xml:"heatingSetpoint,omitempty"`
}

// UnmarshalXML applies Offset's rules to the two set points.
func (sp *SetPoint) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v SetPoint
	err := decodeOptionalChildren(d, start, "SetPoint", []string{"coolingSetpoint", "heatingSetpoint"}, func(name, text string) error {
		n, err := parseBounded(name, text, math.MinInt16, math.MaxInt16, narrowInt16)
		if name == "coolingSetpoint" {
			v.CoolingSetpoint = &n
		} else {
			v.HeatingSetpoint = &n
		}
		return err
	})
	if err != nil {
		return err
	}
	*sp = v
	return nil
}

// Copy returns an independent copy.
func (s SetPoint) Copy() SetPoint {
	c := s
	c.CoolingSetpoint = clonePtr(s.CoolingSetpoint)
	c.HeatingSetpoint = clonePtr(s.HeatingSetpoint)
	return c
}

// OverrideDuration is the overrideDuration element, a UInt16 number of
// seconds. An empty element is refused rather than read as 0.
type OverrideDuration uint16

// UnmarshalXML refuses an empty, nested or out-of-range value.
func (o *OverrideDuration) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	text, err := simpleText(d)
	if err != nil {
		return fmt.Errorf("sep2: %s: %w", start.Name.Local, err)
	}
	n, err := parseUint(text, 16)
	if err != nil {
		return fmt.Errorf("sep2: %s: %w", start.Name.Local, err)
	}
	*o = OverrideDuration(n)
	return nil
}

// TargetReduction is a recommended consumption threshold. Type is UnitType
// (UInt8); Value is UInt16.
type TargetReduction struct {
	Type  uint8  `xml:"type"`
	Value uint16 `xml:"value"`
}

// Copy returns an independent copy.
func (t TargetReduction) Copy() TargetReduction { return t }

// UnmarshalXML requires type then value and refuses a reserved UnitType.
func (t *TargetReduction) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	typ, val, err := decodeUnitValue(d, start, "TargetReduction")
	if err != nil {
		return err
	}
	*t = TargetReduction{Type: typ, Value: val}
	return nil
}

// AppliedTargetReduction is the TargetReduction value a device applied
// (sep.xsd:456-471), carried by DrResponse. It is TargetReduction under the
// name the schema gives the element; the element name comes from the field tag,
// so there is one type and one decode rule.
type AppliedTargetReduction = TargetReduction

// decodeUnitValue decodes the type (UnitType) and value (UInt16) pair of a
// TargetReduction.
func decodeUnitValue(d *xml.Decoder, start xml.StartElement, typeName string) (typ uint8, val uint16, err error) {
	err = decodeChildren(d, start, typeName, []string{"type", "value"}, func(name, text string) error {
		if name == "value" {
			n, err := parseUint(text, 16)
			val = uint16(n)
			return err
		}
		n, err := parseUint(text, 8)
		if err == nil && n > maxUnitType {
			err = fmt.Errorf("%d is a reserved UnitType, want 0 to %d", n, maxUnitType)
		}
		typ = uint8(n)
		return err
	})
	return typ, val, err
}
