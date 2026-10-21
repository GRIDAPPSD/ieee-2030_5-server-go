package dercontrol

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func pct(v uint16) *sep2.PerCent {
	p := sep2.PerCent(v)
	return &p
}

func thousandths(displacement uint16, excitation bool) *sep2.FixedPowerFactor {
	return &sep2.FixedPowerFactor{Displacement: displacement, Excitation: excitation, Multiplier: -3}
}

func TestDecodeFunctions_ValuesAndRefusals(t *testing.T) {
	tests := []struct {
		name    string
		members string
		want    *FunctionSet
		refusal *ValueRefusal
	}{
		{"every function", `{"opModConnect":true,"opModEnergize":false,"opModMaxLimW":10000,"opModFixedPFInjectW":{"displacement":1,"excitation":true},"opModTargetW":{"value":-32768,"multiplier":-9}}`,
			&FunctionSet{OpModConnect: boolPtr(true), OpModEnergize: boolPtr(false), OpModMaxLimW: i64(10000),
				OpModFixedPFInjectW: pfIn(1, true), OpModTargetW: &ActivePowerInput{Value: i64(-32768), Multiplier: i64(-9)}}, nil},
		{"missing multiplier stays missing", `{"opModTargetW":{"value":32767}}`,
			&FunctionSet{OpModTargetW: &ActivePowerInput{Value: i64(32767)}}, nil},
		{"wrong type", `{"opModMaxLimW":"x"}`, nil, &ValueRefusal{"function_value_type", "functions.opModMaxLimW: wrong value type"}},
		{"null", `{"opModConnect":null}`, nil, &ValueRefusal{"function_value_type", "functions.opModConnect: wrong value type"}},
		{"unknown key", `{"opModTargetW":{"value":1,"watts":1}}`, nil, &ValueRefusal{"function_value_type", "functions.opModTargetW: wrong value type"}},
		{"maxLimW above", `{"opModMaxLimW":10001}`, nil, &ValueRefusal{"max_lim_w_range", "maxLimW: must be 0 to 10000"}},
		{"maxLimW below", `{"opModMaxLimW":-1}`, nil, &ValueRefusal{"max_lim_w_range", "maxLimW: must be 0 to 10000"}},
		{"displacement missing", `{"opModFixedPFInjectW":{"excitation":true}}`, nil, &ValueRefusal{"displacement_required", "powerFactor.displacement: required"}},
		{"excitation missing", `{"opModFixedPFInjectW":{"displacement":900}}`, nil, &ValueRefusal{"excitation_required", "powerFactor.excitation: required"}},
		{"displacement below", `{"opModFixedPFInjectW":{"displacement":0,"excitation":true}}`, nil, &ValueRefusal{"displacement_range", "powerFactor.displacement: must be 1 to 1000"}},
		{"displacement above", `{"opModFixedPFInjectW":{"displacement":1001,"excitation":true}}`, nil, &ValueRefusal{"displacement_range", "powerFactor.displacement: must be 1 to 1000"}},
		{"value missing", `{"opModTargetW":{"multiplier":1}}`, nil, &ValueRefusal{"target_w_value_required", "targetW.value: required"}},
		{"value above int16", `{"opModTargetW":{"value":32768}}`, nil, &ValueRefusal{"target_w_value_range", "targetW.value: must be -32768 to 32767"}},
		{"multiplier above", `{"opModTargetW":{"value":1,"multiplier":10}}`, nil, &ValueRefusal{"target_w_multiplier_range", "targetW.multiplier: must be -9 to 9"}},
		{"multiplier below", `{"opModTargetW":{"value":1,"multiplier":-10}}`, nil, &ValueRefusal{"target_w_multiplier_range", "targetW.multiplier: must be -9 to 9"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var members map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.members), &members); err != nil {
				t.Fatal(err)
			}
			got, ref := decodeNoCurves(t, members)
			if !reflect.DeepEqual(ref, tc.refusal) {
				t.Fatalf("refusal = %+v, want %+v", ref, tc.refusal)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("draft = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTakeLegacy_FillsTheRequestOrRefuses(t *testing.T) {
	i := func(v int64) *int64 { return &v }
	tests := []struct {
		name    string
		typ     string
		values  LegacyValues
		want    CreateRequest
		attrs   []LogAttr
		refusal *ValueRefusal
	}{
		{"connect", "connect", LegacyValues{}, CreateRequest{Type: Connect}, nil, nil},
		{"maxLimW", "maxLimW", LegacyValues{MaxLimW: i(6000)}, CreateRequest{Type: MaxLimW, MaxLimW: uint16ptr(6000)}, []LogAttr{{"value", uint16(6000)}}, nil},
		{"fixedPF", "fixedPFInjectW", LegacyValues{PowerFactor: &PowerFactorInput{Displacement: i(900), Excitation: boolPtr(false)}},
			CreateRequest{Type: FixedPFInjectW, PowerFactor: &PowerFactorValue{Displacement: 900, Excitation: boolPtr(false)}}, []LogAttr{{"value", uint16(900)}, {"excitation", false}}, nil},
		{"targetW", "targetW", LegacyValues{TargetW: &ActivePowerInput{Value: i(-5)}},
			CreateRequest{Type: TargetW, TargetW: &sep2.ActivePower{Value: -5}}, []LogAttr{{"value", int16(-5)}, {"multiplier", int8(0)}}, nil},
		{"required", "maxLimW", LegacyValues{}, CreateRequest{Type: MaxLimW}, nil, &ValueRefusal{"max_lim_w_required", "maxLimW: required for type maxLimW"}},
		{"pf required", "fixedPFInjectW", LegacyValues{}, CreateRequest{Type: FixedPFInjectW}, nil, &ValueRefusal{"power_factor_required", "powerFactor: required for type fixedPFInjectW"}},
		{"targetW refused before maxLimW", "connect", LegacyValues{MaxLimW: i(1), TargetW: &ActivePowerInput{Value: i(1)}}, CreateRequest{Type: Connect}, nil,
			&ValueRefusal{"target_w_not_allowed", "targetW: not allowed for this type"}},
		{"maxLimW refused before powerFactor", "targetW", LegacyValues{MaxLimW: i(1), PowerFactor: &PowerFactorInput{}}, CreateRequest{Type: TargetW}, nil,
			&ValueRefusal{"max_lim_w_not_allowed", "maxLimW: not allowed for this type"}},
		{"powerFactor not allowed", "maxLimW", LegacyValues{MaxLimW: i(1), PowerFactor: &PowerFactorInput{}}, CreateRequest{Type: MaxLimW}, nil,
			&ValueRefusal{"power_factor_not_allowed", "powerFactor: not allowed for this type"}},
		{"range", "maxLimW", LegacyValues{MaxLimW: i(10001)}, CreateRequest{Type: MaxLimW}, nil, &ValueRefusal{"max_lim_w_range", "maxLimW: must be 0 to 10000"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var req CreateRequest
			known, attrs, ref := TakeLegacy(tc.typ, tc.values, &req)
			if !known || !reflect.DeepEqual(ref, tc.refusal) || !reflect.DeepEqual(attrs, tc.attrs) || !reflect.DeepEqual(req, tc.want) {
				t.Fatalf("known %v attrs %+v refusal %+v req %+v; want attrs %+v refusal %+v req %+v", known, attrs, ref, req, tc.attrs, tc.refusal, tc.want)
			}
		})
	}
	var req CreateRequest
	if known, _, _ := TakeLegacy("opModFixedW", LegacyValues{}, &req); known || req.Type != "" {
		t.Fatalf("unknown type: known %v, Type %q", known, req.Type)
	}
}

// A body carrying a functions set takes no legacy value member; the first
// one present is named, in the order they have always been refused.
func TestRefuseBesideFunctions(t *testing.T) {
	one := int64(1)
	for _, tc := range []struct {
		values LegacyValues
		want   *ValueRefusal
	}{
		{LegacyValues{}, nil},
		{LegacyValues{MaxLimW: &one, TargetW: &ActivePowerInput{}}, &ValueRefusal{"max_lim_w_beside_functions", "maxLimW: not allowed with functions"}},
		{LegacyValues{PowerFactor: &PowerFactorInput{}, TargetW: &ActivePowerInput{}}, &ValueRefusal{"power_factor_beside_functions", "powerFactor: not allowed with functions"}},
		{LegacyValues{TargetW: &ActivePowerInput{}}, &ValueRefusal{"target_w_beside_functions", "targetW: not allowed with functions"}},
	} {
		if got := RefuseBesideFunctions(tc.values); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%+v: got %+v, want %+v", tc.values, got, tc.want)
		}
	}
}

func TestViewsAndLogFollowTheRows(t *testing.T) {
	b := &sep2.DERControlBase{OpModConnect: boolPtr(true), OpModMaxLimW: pct(5000), OpModFixedPFInjectW: thousandths(950, true), OpModTargetW: &sep2.ActivePower{Value: 7, Multiplier: 2}}
	m := uint16(5000)
	wantBase := DERControlBaseView{OpModConnect: boolPtr(true), OpModMaxLimW: &m,
		OpModFixedPFInjectW: &FixedPowerFactorView{Displacement: 950, Excitation: true, Multiplier: -3},
		OpModTargetW:        &ActivePowerView{Value: 7, Multiplier: 2}}
	if got := BaseView(b); !reflect.DeepEqual(got, wantBase) {
		t.Errorf("BaseView = %+v, want %+v", got, wantBase)
	}
	// The functions view reports each value as a request gives it.
	wantFns := `{"opModConnect":true,"opModFixedPFInjectW":{"displacement":950,"excitation":true},"opModMaxLimW":5000,"opModTargetW":{"value":7,"multiplier":2}}`
	if got, err := json.Marshal(FunctionValues(b)); err != nil || string(got) != wantFns {
		t.Errorf("FunctionValues = %s (%v), want %s", got, err, wantFns)
	}
	if got := FunctionValues(nil); got == nil || len(got) != 0 {
		t.Errorf("FunctionValues(nil) = %#v, want an empty map", got)
	}
	// The first function in catalog order that logs a value is maxLimW.
	if got, want := LogAttrs(b), []LogAttr{{"value", uint16(5000)}}; !reflect.DeepEqual(got, want) {
		t.Errorf("LogAttrs = %+v, want %+v", got, want)
	}
	tw := &sep2.DERControlBase{OpModEnergize: boolPtr(true), OpModTargetW: &sep2.ActivePower{Value: 7, Multiplier: 2}}
	if got, want := LogAttrs(tw), []LogAttr{{"value", int16(7)}, {"multiplier", int8(2)}}; !reflect.DeepEqual(got, want) {
		t.Errorf("LogAttrs = %+v, want %+v", got, want)
	}
	if got := LogAttrs(&sep2.DERControlBase{OpModConnect: boolPtr(true)}); got != nil {
		t.Errorf("LogAttrs(connect) = %+v, want none", got)
	}
}

// An unknown type is refused as unknown whatever value members it carries,
// as the HTTP path refuses it before reading any.
func TestBuildBase_UnknownTypeBeforeItsMembers(t *testing.T) {
	_, err := buildBase(CreateRequest{Type: ControlType("opModFixedW"), TargetW: &sep2.ActivePower{Value: 1}, MaxLimW: uint16ptr(1)})
	assertRefusal(t, err, RefusalUnknownType)
}

// A Go caller's functions set is checked as a request's is: a member it
// leaves out is refused as missing, never read as zero, and the refusal
// carries the member's own wording.
func TestBuildBase_FunctionSetMissingMembers(t *testing.T) {
	tests := []struct {
		name   string
		set    FunctionSet
		code   RefusalCode
		detail ValueRefusal
	}{
		{"no excitation", FunctionSet{OpModFixedPFInjectW: &PowerFactorInput{Displacement: i64(900)}}, RefusalMissingValue,
			ValueRefusal{"excitation_required", "powerFactor.excitation: required"}},
		{"no displacement", FunctionSet{OpModFixedPFInjectW: &PowerFactorInput{Excitation: boolPtr(false)}}, RefusalMissingValue,
			ValueRefusal{"displacement_required", "powerFactor.displacement: required"}},
		{"no target value", FunctionSet{OpModTargetW: &ActivePowerInput{Multiplier: i64(1)}}, RefusalMissingValue,
			ValueRefusal{"target_w_value_required", "targetW.value: required"}},
		{"displacement out of range", FunctionSet{OpModFixedPFInjectW: pfIn(1001, true)}, RefusalValueOutOfRange,
			ValueRefusal{"displacement_range", "powerFactor.displacement: must be 1 to 1000"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildBase(CreateRequest{Functions: &tc.set})
			var ref *RefusalError
			if !errors.As(err, &ref) || ref.Code != tc.code || ref.Detail == nil || *ref.Detail != tc.detail {
				t.Fatalf("err = %v (%+v), want %s with %+v", err, ref, tc.code, tc.detail)
			}
		})
	}
}

// decodeNoCurves is DecodeFunctions with no curve store, which holds no
// curve, so it decodes everything but a curve value as the handler would.
func decodeNoCurves(t *testing.T, members map[string]json.RawMessage) (*FunctionSet, *ValueRefusal) {
	t.Helper()
	draft, ref, err := DecodeFunctions(t.Context(), members, nil)
	if err != nil {
		t.Fatalf("DecodeFunctions with no curve store: %v", err)
	}
	return draft, ref
}
