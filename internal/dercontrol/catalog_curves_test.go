package dercontrol

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"reflect"
	"strconv"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// curveTypes is each curve row with the DERCurveType core gives its element.
var curveTypes = []struct {
	name      string
	curveType uint8
}{
	{FuncFreqWatt, sep2.CurveTypeOpModFreqWatt},
	{FuncHFRTMayTrip, sep2.CurveTypeOpModHFRTMayTrip},
	{FuncHFRTMustTrip, sep2.CurveTypeOpModHFRTMustTrip},
	{FuncHVRTMayTrip, sep2.CurveTypeOpModHVRTMayTrip},
	{FuncHVRTMomentaryCessation, sep2.CurveTypeOpModHVRTMomentaryCessation},
	{FuncHVRTMustTrip, sep2.CurveTypeOpModHVRTMustTrip},
	{FuncLFRTMayTrip, sep2.CurveTypeOpModLFRTMayTrip},
	{FuncLFRTMustTrip, sep2.CurveTypeOpModLFRTMustTrip},
	{FuncLVRTMayTrip, sep2.CurveTypeOpModLVRTMayTrip},
	{FuncLVRTMomentaryCessation, sep2.CurveTypeOpModLVRTMomentaryCessation},
	{FuncLVRTMustTrip, sep2.CurveTypeOpModLVRTMustTrip},
	{FuncVoltVar, sep2.CurveTypeOpModVoltVar},
	{FuncVoltWatt, sep2.CurveTypeOpModVoltWatt},
	{FuncWattPF, sep2.CurveTypeOpModWattPF},
	{FuncWattVar, sep2.CurveTypeOpModWattVar},
}

// curveStore holds one curve of every type, stored as "c<type>".
func curveStore(t *testing.T) *memory.Store[sep2.DERCurve] {
	t.Helper()
	s := memory.NewStore[sep2.DERCurve]()
	for _, c := range curveTypes {
		id := "c" + strconv.Itoa(int(c.curveType))
		curve := sep2.DERCurve{MRID: id, CurveType: c.curveType}
		curve.Href = "/dc/" + id
		if err := s.Create(context.Background(), id, curve); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// Each curve row serves kind curve and its element's curveType.
func TestCurveRows_Descriptor(t *testing.T) {
	byName := map[string]FunctionInfo{}
	for _, f := range Catalog() {
		byName[f.Name] = f
	}
	for _, c := range curveTypes {
		f, ok := byName[c.name]
		if !ok {
			t.Errorf("%s: no catalog row", c.name)
			continue
		}
		if f.Kind != "curve" || f.CurveType == nil || *f.CurveType != c.curveType {
			t.Errorf("%s: descriptor %+v, want kind curve, curveType %d", c.name, f.Descriptor, c.curveType)
		}
	}
}

// A curve row with a stored curve of its type builds the link to that
// curve, and reports the curve's id and href back in the two views.
func TestCurveRows_StoredCurveRoundTrips(t *testing.T) {
	curves := curveStore(t)
	for _, c := range curveTypes {
		t.Run(c.name, func(t *testing.T) {
			id := "c" + strconv.Itoa(int(c.curveType))
			draft, ref, err := DecodeFunctions(t.Context(), map[string]json.RawMessage{c.name: json.RawMessage(strconv.Quote(id))}, curves)
			if err != nil || ref != nil {
				t.Fatalf("decode: refusal %+v, error %v", ref, err)
			}
			base, err := buildBase(CreateRequest{Functions: draft})
			if err != nil {
				t.Fatalf("buildBase: %v", err)
			}
			got, err := xml.Marshal(base)
			if err != nil {
				t.Fatal(err)
			}
			if want := `<DERControlBase><` + c.name + ` href="/dc/` + id + `"></` + c.name + `></DERControlBase>`; string(got) != want {
				t.Errorf("wire bytes:\n got  %s\n want %s", got, want)
			}
			fns, err := json.Marshal(FunctionValues(base))
			if err != nil {
				t.Fatal(err)
			}
			if want := `{"` + c.name + `":"` + id + `"}`; string(fns) != want {
				t.Errorf("functions view = %s, want %s", fns, want)
			}
			view, err := json.Marshal(BaseView(base))
			if err != nil {
				t.Fatal(err)
			}
			if want := `{"` + c.name + `":{"href":"/dc/` + id + `"}}`; string(view) != want {
				t.Errorf("derControlBase view = %s, want %s", view, want)
			}
			if want := []LogAttr{{"curve", "/dc/" + id}}; !reflect.DeepEqual(LogAttrs(base), want) {
				t.Errorf("LogAttrs = %+v, want %+v", LogAttrs(base), want)
			}
		})
	}
}

// A curve value that names no stored curve, or a curve of another type, is
// refused on every curve row, and nothing is decoded.
func TestCurveRows_Refusals(t *testing.T) {
	curves := curveStore(t)
	for _, c := range curveTypes {
		other := "c" + strconv.Itoa(int((c.curveType+1)%15))
		path := "functions." + c.name
		for _, tc := range []struct {
			name, value string
			curves      CurveReader
			want        ValueRefusal
		}{
			{"unknown id", `"nope"`, curves, ValueRefusal{"unknown_curve", path + ": no stored curve has this id"}},
			{"no curve store", strconv.Quote("c" + strconv.Itoa(int(c.curveType))), nil, ValueRefusal{"unknown_curve", path + ": no stored curve has this id"}},
			{"wrong type", strconv.Quote(other), curves, ValueRefusal{"wrong_curve_type", path + ": the curve's curveType must be " + strconv.Itoa(int(c.curveType))}},
			{"blank id", `" "`, curves, ValueRefusal{"function_value_required", path + ": required"}},
			{"not a string", `11`, curves, ValueRefusal{"function_value_type", path + ": wrong value type"}},
		} {
			draft, ref, err := DecodeFunctions(t.Context(), map[string]json.RawMessage{c.name: json.RawMessage(tc.value)}, tc.curves)
			if err != nil || draft != nil || ref == nil || *ref != tc.want {
				t.Errorf("%s %s: draft %+v refusal %+v error %v, want %+v", c.name, tc.name, draft, ref, err, tc.want)
			}
		}
	}
}

type failingCurves struct{ err error }

func (f failingCurves) Get(context.Context, string) (sep2.DERCurve, error) {
	return sep2.DERCurve{}, f.err
}

// A curve store that cannot be read decides nothing: the error is returned
// and no refusal is made, so the caller answers 500 rather than 400.
func TestCurveRows_StoreErrorIsNotARefusal(t *testing.T) {
	cause := errors.New("disk gone")
	draft, ref, err := DecodeFunctions(t.Context(), map[string]json.RawMessage{FuncVoltVar: json.RawMessage(`"c11"`)}, failingCurves{cause})
	if !errors.Is(err, cause) || ref != nil || draft != nil {
		t.Errorf("draft %+v refusal %+v error %v, want only an error wrapping %v", draft, ref, err, cause)
	}
}
