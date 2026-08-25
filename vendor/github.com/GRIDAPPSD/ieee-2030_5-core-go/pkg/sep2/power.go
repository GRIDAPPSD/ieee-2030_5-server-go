package sep2

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
