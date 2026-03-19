package sep2

// ActivePower represents real power in watts.
type ActivePower struct {
	Multiplier int8  `xml:"multiplier"`
	Value      int64 `xml:"value"`
}

// ReactivePower represents reactive power in volt-amperes reactive.
type ReactivePower struct {
	Multiplier int8  `xml:"multiplier"`
	Value      int64 `xml:"value"`
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
