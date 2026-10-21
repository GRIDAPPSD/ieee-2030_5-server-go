package dercontrol

import (
	"context"
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/sep2time"
)

func boolPtr(v bool) *bool { return &v }

func TestIssue_Functions_TwoFunctionsCarryBothElements(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	res, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = ""
		r.Functions = &FunctionSet{
			MaxLimW:     uint16ptr(5000),
			PowerFactor: &PowerFactorValue{Displacement: 900, Excitation: boolPtr(true)},
		}
	})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	b := res.Control.DERControlBase
	if b == nil || b.OpModMaxLimW == nil || *b.OpModMaxLimW != 5000 {
		t.Fatalf("OpModMaxLimW = %v, want 5000", b)
	}
	pf := b.OpModFixedPFInjectW
	if pf == nil || pf.Displacement != 900 || !pf.Excitation || pf.Multiplier != -3 {
		t.Fatalf("OpModFixedPFInjectW = %+v, want displacement 900 excitation true multiplier -3", pf)
	}
	if b.OpModConnect != nil || b.OpModEnergize != nil || b.OpModTargetW != nil {
		t.Fatalf("base carries elements that were not requested: %+v", b)
	}
}

func TestIssue_Functions_ConnectAndEnergizeAreIndependent(t *testing.T) {
	h := newHarness(t, Config{PEN: testPEN(1)})
	h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))

	res, err := issueWith(t, h, func(r *CreateRequest) {
		r.Type = ""
		r.Functions = &FunctionSet{Connect: boolPtr(true), Energize: boolPtr(false)}
	})
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	b := res.Control.DERControlBase
	if b.OpModConnect == nil || !*b.OpModConnect || b.OpModEnergize == nil || *b.OpModEnergize {
		t.Fatalf("connect = %v energize = %v, want true and false", b.OpModConnect, b.OpModEnergize)
	}
}

func TestBuildBase_LegacyConnectStillSetsBothElements(t *testing.T) {
	for _, tc := range []struct {
		typ  ControlType
		want bool
	}{{Connect, true}, {Disconnect, false}} {
		b, err := buildBase(CreateRequest{Type: tc.typ})
		if err != nil {
			t.Fatalf("buildBase(%s): %v", tc.typ, err)
		}
		if b.OpModConnect == nil || *b.OpModConnect != tc.want || b.OpModEnergize == nil || *b.OpModEnergize != tc.want {
			t.Errorf("%s: connect = %v energize = %v, want both %v", tc.typ, b.OpModConnect, b.OpModEnergize, tc.want)
		}
	}
}

func TestBuildBase_FunctionsRefusals(t *testing.T) {
	tests := []struct {
		name string
		req  CreateRequest
		want RefusalCode
	}{
		{"type and functions", CreateRequest{Type: Connect, Functions: &FunctionSet{Connect: boolPtr(true)}}, RefusalTypeAndFunctions},
		{"empty functions", CreateRequest{Functions: &FunctionSet{}}, RefusalMissingValue},
		{"maxLimW above range", CreateRequest{Functions: &FunctionSet{MaxLimW: uint16ptr(10001)}}, RefusalValueOutOfRange},
		{"pf displacement zero", CreateRequest{Functions: &FunctionSet{PowerFactor: &PowerFactorValue{Displacement: 0, Excitation: boolPtr(false)}}}, RefusalValueOutOfRange},
		{"pf without excitation", CreateRequest{Functions: &FunctionSet{PowerFactor: &PowerFactorValue{Displacement: 900}}}, RefusalMissingValue},
		{"targetW multiplier out of range", CreateRequest{Functions: &FunctionSet{TargetW: &sep2.ActivePower{Value: 1, Multiplier: 10}}}, RefusalValueOutOfRange},
		{"top-level value beside functions", CreateRequest{Functions: &FunctionSet{Connect: boolPtr(true)}, MaxLimW: uint16ptr(1)}, RefusalUnexpectedValue},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildBase(tc.req)
			assertRefusal(t, err, tc.want)
		})
	}
}

func TestIssue_Supersede_FunctionSets(t *testing.T) {
	two := func(r *CreateRequest) {
		r.Type = ""
		r.Functions = &FunctionSet{MaxLimW: uint16ptr(5000), PowerFactor: &PowerFactorValue{Displacement: 900, Excitation: boolPtr(false)}}
	}
	tests := []struct {
		name      string
		first     func(*CreateRequest)
		second    func(*CreateRequest)
		supersede bool
	}{
		{"equal two-function sets supersede", two, func(r *CreateRequest) {
			r.Type = ""
			r.Functions = &FunctionSet{MaxLimW: uint16ptr(4000), PowerFactor: &PowerFactorValue{Displacement: 800, Excitation: boolPtr(true)}}
		}, true},
		{"single function against a superset does not", func(r *CreateRequest) {
			r.Type = MaxLimW
			r.MaxLimW = uint16ptr(5000)
		}, two, false},
		{"superset against its subset does not", two, func(r *CreateRequest) {
			r.Type = MaxLimW
			r.MaxLimW = uint16ptr(5000)
		}, false},
		{"legacy type and equal functions supersede", func(r *CreateRequest) {
			r.Type = MaxLimW
			r.MaxLimW = uint16ptr(5000)
		}, func(r *CreateRequest) {
			r.Type = ""
			r.Functions = &FunctionSet{MaxLimW: uint16ptr(3000)}
		}, true},
		{"legacy connect against connect and energize functions supersede", func(r *CreateRequest) {
			r.Type = Connect
		}, func(r *CreateRequest) {
			r.Type = ""
			r.Functions = &FunctionSet{Connect: boolPtr(false), Energize: boolPtr(false)}
		}, true},
		{"connect alone against connect and energize does not", func(r *CreateRequest) {
			r.Type = ""
			r.Functions = &FunctionSet{Connect: boolPtr(true)}
		}, func(r *CreateRequest) {
			r.Type = Disconnect
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{PEN: testPEN(1)})
			h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
			start := sep2time.Now().Unix() + 1000
			first, err := issueWith(t, h, func(r *CreateRequest) {
				tc.first(r)
				r.Start, r.DurationSeconds = &start, 1000
			})
			if err != nil {
				t.Fatalf("issue first: %v", err)
			}
			second, err := issueWith(t, h, func(r *CreateRequest) {
				tc.second(r)
				r.Start, r.DurationSeconds = &start, 1000
			})
			if err != nil {
				t.Fatalf("issue second: %v", err)
			}
			lc, err := h.lifecycles.Get(context.Background(), "dev1/0/p1", first.ID)
			if err != nil {
				t.Fatalf("load first lifecycle: %v", err)
			}
			if tc.supersede {
				if !reflect.DeepEqual(second.Supersedes, []string{first.Control.MRID}) || lc.SupersededBy != second.Control.MRID {
					t.Fatalf("Supersedes = %v, SupersededBy = %q, want first %q superseded by %q", second.Supersedes, lc.SupersededBy, first.Control.MRID, second.Control.MRID)
				}
				return
			}
			if len(second.Supersedes) != 0 || lc.SupersededAt != nil {
				t.Fatalf("Supersedes = %v, SupersededAt = %v, want nothing superseded", second.Supersedes, lc.SupersededAt)
			}
		})
	}
}

func TestCatalog_ListsTheFourBuiltFunctions(t *testing.T) {
	var names []string
	for _, f := range Catalog() {
		names = append(names, f.Name)
		if f.Value == "" {
			t.Errorf("%s: empty value shape", f.Name)
		}
	}
	want := []string{"opModConnect", "opModEnergize", "opModMaxLimW", "opModFixedPFInjectW", "opModTargetW"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("catalog names = %v, want %v", names, want)
	}
}

func TestFunctionNames_ReadsTheElementsPresent(t *testing.T) {
	tr := true
	v := sep2.PerCent(10)
	b := &sep2.DERControlBase{OpModConnect: &tr, OpModMaxLimW: &v}
	if got, want := FunctionNames(b), []string{"opModConnect", "opModMaxLimW"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("FunctionNames = %v, want %v", got, want)
	}
	if got := FunctionNames(nil); len(got) != 0 {
		t.Fatalf("FunctionNames(nil) = %v, want none", got)
	}
}
