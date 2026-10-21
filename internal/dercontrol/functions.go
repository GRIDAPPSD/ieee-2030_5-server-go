package dercontrol

import (
	"strings"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

// Function names are the IEEE 2030.5-2018 DERControlBase element names, so
// the catalog, the request body and the wire share one vocabulary.
const (
	FuncConnect        = "opModConnect"
	FuncEnergize       = "opModEnergize"
	FuncMaxLimW        = "opModMaxLimW"
	FuncFixedPFInjectW = "opModFixedPFInjectW"
	FuncTargetW        = "opModTargetW"
)

// FunctionSet is the set of control functions one control carries. A nil
// member is absent from the control. Connect and Energize are independent:
// only the legacy Connect and Disconnect types set both.
type FunctionSet struct {
	Connect     *bool
	Energize    *bool
	MaxLimW     *uint16
	PowerFactor *PowerFactorValue
	TargetW     *sep2.ActivePower
}

// FunctionInfo describes one catalog function for GET
// /api/der/control-functions. Value names the request value's shape.
type FunctionInfo struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description"`
}

var catalog = []FunctionInfo{
	{FuncConnect, "boolean", "Connect (true) or disconnect (false) the DER."},
	{FuncEnergize, "boolean", "Energize (true) or de-energize (false) the DER."},
	{FuncMaxLimW, "integer 0 to 10000", "Active power ceiling in hundredths of a percent of setMaxW."},
	{FuncFixedPFInjectW, "object {displacement 1 to 1000, excitation boolean}", "Fixed power factor while injecting active power; displacement is thousandths."},
	{FuncTargetW, "object {value int16, multiplier -9 to 9}", "Active power target, value x 10^multiplier watts, discharge positive."},
}

// Catalog returns the functions a control can carry, in a stable order.
func Catalog() []FunctionInfo {
	return append([]FunctionInfo(nil), catalog...)
}

// FunctionNames lists the catalog functions b carries, in catalog order.
func FunctionNames(b *sep2.DERControlBase) []string {
	var names []string
	if b == nil {
		return names
	}
	if b.OpModConnect != nil {
		names = append(names, FuncConnect)
	}
	if b.OpModEnergize != nil {
		names = append(names, FuncEnergize)
	}
	if b.OpModMaxLimW != nil {
		names = append(names, FuncMaxLimW)
	}
	if b.OpModFixedPFInjectW != nil {
		names = append(names, FuncFixedPFInjectW)
	}
	if b.OpModTargetW != nil {
		names = append(names, FuncTargetW)
	}
	return names
}

// functionSetKey reduces b to its function set for the supersede scan's
// equality test: equal keys mean equal sets (2018 rule t). A base carrying
// an element outside the catalog keys as "other".
func functionSetKey(b *sep2.DERControlBase) string {
	if b == nil {
		return ""
	}
	if names := FunctionNames(b); len(names) > 0 {
		return strings.Join(names, ",")
	}
	return "other"
}

// baseFromFunctions builds the DERControlBase for f, applying the same range
// checks as the legacy types.
func baseFromFunctions(f *FunctionSet) (*sep2.DERControlBase, error) {
	b := &sep2.DERControlBase{}
	empty := true
	if f.Connect != nil {
		v := *f.Connect
		b.OpModConnect, empty = &v, false
	}
	if f.Energize != nil {
		v := *f.Energize
		b.OpModEnergize, empty = &v, false
	}
	if f.MaxLimW != nil {
		if *f.MaxLimW > 10000 {
			return nil, refuse(RefusalValueOutOfRange)
		}
		v := sep2.PerCent(*f.MaxLimW)
		b.OpModMaxLimW, empty = &v, false
	}
	if pf := f.PowerFactor; pf != nil {
		if pf.Excitation == nil {
			return nil, refuse(RefusalMissingValue)
		}
		if pf.Displacement < 1 || pf.Displacement > 1000 {
			return nil, refuse(RefusalValueOutOfRange)
		}
		b.OpModFixedPFInjectW = &sep2.FixedPowerFactor{Displacement: pf.Displacement, Excitation: *pf.Excitation, Multiplier: -3}
		empty = false
	}
	if tw := f.TargetW; tw != nil {
		if tw.Multiplier < -9 || tw.Multiplier > 9 {
			return nil, refuse(RefusalValueOutOfRange)
		}
		v := *tw
		b.OpModTargetW, empty = &v, false
	}
	if empty {
		return nil, refuse(RefusalMissingValue)
	}
	return b, nil
}
