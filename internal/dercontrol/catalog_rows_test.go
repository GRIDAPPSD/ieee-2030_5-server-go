package dercontrol

import (
	"encoding/json"
	"encoding/xml"
	"reflect"
	"strings"
	"testing"
)

// #969: each row added after the first five builds the element it names,
// with the value sent, and reports that value back in both views.
func TestAddedRows_WireBytesAndViews(t *testing.T) {
	tests := []struct {
		name, value, wire string
		view              any
	}{
		{FuncFixedPFAbsorbW, `{"displacement":950,"excitation":false}`,
			`<opModFixedPFAbsorbW><displacement>950</displacement><excitation>false</excitation><multiplier>-3</multiplier></opModFixedPFAbsorbW>`,
			&FixedPowerFactorView{Displacement: 950, Multiplier: -3}},
		{FuncFixedVar, `{"refType":2,"value":-2500}`,
			`<opModFixedVar><refType>2</refType><value>-2500</value></opModFixedVar>`,
			&FixedVarView{RefType: 2, Value: -2500}},
		{FuncFixedW, `-7500`, `<opModFixedW>-7500</opModFixedW>`, int16ptr(-7500)},
		{FuncFreqDroop, `{"dBOF":36,"dBUF":37,"kOF":5,"kUF":6,"openLoopTms":500}`,
			`<opModFreqDroop><dBOF>36</dBOF><dBUF>37</dBUF><kOF>5</kOF><kUF>6</kUF><openLoopTms>500</openLoopTms></opModFreqDroop>`,
			&FreqDroopView{DBOF: 36, DBUF: 37, KOF: 5, KUF: 6, OpenLoopTms: 500}},
		{FuncTargetVar, `{"value":-1200,"multiplier":2}`,
			`<opModTargetVar><multiplier>2</multiplier><value>-1200</value></opModTargetVar>`,
			&ActivePowerView{Value: -1200, Multiplier: 2}},
		{FuncRampTms, `6000`, `<rampTms>6000</rampTms>`, uint16ptr(6000)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			draft, ref := DecodeFunctions(map[string]json.RawMessage{tc.name: json.RawMessage(tc.value)})
			if ref != nil {
				t.Fatalf("decode refused: %+v", ref)
			}
			base, err := buildBase(CreateRequest{Functions: draft})
			if err != nil {
				t.Fatalf("buildBase: %v", err)
			}
			got, err := xml.Marshal(base)
			if err != nil {
				t.Fatal(err)
			}
			if want := "<DERControlBase>" + tc.wire + "</DERControlBase>"; string(got) != want {
				t.Errorf("wire bytes:\n got  %s\n want %s", got, want)
			}
			if names := FunctionNames(base); !reflect.DeepEqual(names, []string{tc.name}) {
				t.Errorf("FunctionNames = %v, want [%s]", names, tc.name)
			}
			fns, err := json.Marshal(FunctionValues(base))
			if err != nil {
				t.Fatal(err)
			}
			if want := `{"` + tc.name + `":` + tc.value + `}`; string(fns) != want {
				t.Errorf("functions view = %s, want %s", fns, want)
			}
			view := reflect.ValueOf(BaseView(base)).FieldByNameFunc(func(f string) bool {
				field, _ := reflect.TypeFor[DERControlBaseView]().FieldByName(f)
				tag, _, _ := strings.Cut(field.Tag.Get("json"), ",")
				return tag == tc.name
			})
			if !view.IsValid() || !reflect.DeepEqual(view.Interface(), tc.view) {
				t.Errorf("derControlBase view %s = %+v, want %+v", tc.name, view, tc.view)
			}
			if TypeName(base) != "combined" {
				t.Errorf("TypeName = %q, want combined", TypeName(base))
			}
		})
	}
}

// The catalog's integer bounds are a second copy of what core enforces when
// it encodes an element, so every served min and max must encode: a bound
// wider than core's fails here instead of at serve time.
func TestServedIntegerBoundsEncode(t *testing.T) {
	bounds := integerBounds(t)
	for _, b := range bounds {
		for _, v := range []int64{b.min, b.max} {
			name, _, _ := strings.Cut(b.where, ".")
			draft, ref := DecodeFunctions(map[string]json.RawMessage{name: json.RawMessage(b.body(v))})
			if ref != nil {
				t.Errorf("%s = %d: refused %+v", b.where, v, ref)
				continue
			}
			base, err := buildBase(CreateRequest{Functions: draft})
			if err != nil {
				t.Errorf("%s = %d: buildBase: %v", b.where, v, err)
				continue
			}
			if _, err := xml.Marshal(base); err != nil {
				t.Errorf("%s = %d: core refuses to encode it: %v", b.where, v, err)
			}
		}
	}
}

func TestAddedRows_Refusals(t *testing.T) {
	tests := []struct {
		members string
		refusal ValueRefusal
	}{
		{`{"opModFixedW":10001}`, ValueRefusal{"function_value_range", "functions.opModFixedW: must be -10000 to 10000"}},
		{`{"opModFixedW":-10001}`, ValueRefusal{"function_value_range", "functions.opModFixedW: must be -10000 to 10000"}},
		{`{"opModFixedVar":{"refType":0,"value":1}}`, ValueRefusal{"function_value_range", "functions.opModFixedVar.refType: must be 1 to 3"}},
		{`{"opModFixedVar":{"refType":4,"value":1}}`, ValueRefusal{"function_value_range", "functions.opModFixedVar.refType: must be 1 to 3"}},
		{`{"opModFixedVar":{"value":1}}`, ValueRefusal{"function_value_required", "functions.opModFixedVar.refType: required"}},
		{`{"opModFixedVar":{"refType":5}}`, ValueRefusal{"function_value_required", "functions.opModFixedVar.value: required"}},
		{`{"opModFreqDroop":{"dBOF":1,"dBUF":1,"kOF":1,"kUF":1}}`, ValueRefusal{"function_value_required", "functions.opModFreqDroop.openLoopTms: required"}},
		{`{"opModFreqDroop":{"dBOF":4294967296,"dBUF":1,"kOF":1,"kUF":1,"openLoopTms":1}}`, ValueRefusal{"function_value_range", "functions.opModFreqDroop.dBOF: must be 0 to 4294967295"}},
		{`{"opModFreqDroop":{"dBOF":1,"dBUF":1,"kOF":65536,"kUF":1,"openLoopTms":1}}`, ValueRefusal{"function_value_range", "functions.opModFreqDroop.kOF: must be 0 to 65535"}},
		{`{"opModTargetVar":{"multiplier":1}}`, ValueRefusal{"function_value_required", "functions.opModTargetVar.value: required"}},
		{`{"opModTargetVar":{"value":1,"multiplier":10}}`, ValueRefusal{"function_value_range", "functions.opModTargetVar.multiplier: must be -9 to 9"}},
		{`{"rampTms":65536}`, ValueRefusal{"function_value_range", "functions.rampTms: must be 0 to 65535"}},
		{`{"rampTms":-1}`, ValueRefusal{"function_value_range", "functions.rampTms: must be 0 to 65535"}},
		{`{"opModFixedPFAbsorbW":{"displacement":0,"excitation":true}}`, ValueRefusal{"function_value_range", "functions.opModFixedPFAbsorbW.displacement: must be 1 to 1000"}},
	}
	for _, tc := range tests {
		var members map[string]json.RawMessage
		if err := json.Unmarshal([]byte(tc.members), &members); err != nil {
			t.Fatal(err)
		}
		draft, ref := DecodeFunctions(members)
		if draft != nil || ref == nil || *ref != tc.refusal {
			t.Errorf("%s: draft %+v refusal %+v, want %+v", tc.members, draft, ref, tc.refusal)
		}
	}
}

// A control carrying only a droop logs the droop values as its audit value.
func TestFreqDroopAuditValue(t *testing.T) {
	draft, ref := DecodeFunctions(map[string]json.RawMessage{FuncFreqDroop: json.RawMessage(`{"dBOF":36,"dBUF":37,"kOF":5,"kUF":6,"openLoopTms":500}`)})
	if ref != nil {
		t.Fatalf("decode refused: %+v", ref)
	}
	base, err := buildBase(CreateRequest{Functions: draft})
	if err != nil {
		t.Fatal(err)
	}
	want := []LogAttr{{"dBOF", uint32(36)}, {"dBUF", uint32(37)}, {"kOF", uint16(5)}, {"kUF", uint16(6)}, {"openLoopTms", uint16(500)}}
	if got := LogAttrs(base); !reflect.DeepEqual(got, want) {
		t.Errorf("LogAttrs = %+v, want %+v", got, want)
	}
}

func int16ptr(v int16) *int16 { return &v }
