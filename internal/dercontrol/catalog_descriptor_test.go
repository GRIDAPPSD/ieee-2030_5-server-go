package dercontrol

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

// Every kind names itself on the wire and describes a shape of its own, so a
// kind added without a descriptor fails here. Go has no exhaustive enum, so
// the check is a test, not a compile error: kindCount closes the range.
func TestEveryKindHasADescriptor(t *testing.T) {
	samples := map[valueKind]shape{
		kindBoolean: {kind: kindBoolean},
		kindInteger: {kind: kindInteger, bounds: intRange{min: 1, max: 2}},
		kindObject:  {kind: kindObject, fields: []field{{name: "a", shape: shape{kind: kindBoolean}}}},
		kindCurve:   {kind: kindCurve, curveType: 7},
	}
	for k := kindBoolean; k < kindCount; k++ {
		s, ok := samples[k]
		if !ok {
			t.Errorf("kind %d: no sample shape in this test", k)
			continue
		}
		d := s.describe()
		if d.Kind == "" || d.Kind != k.String() {
			t.Errorf("kind %d: descriptor kind %q, want %q", k, d.Kind, k.String())
		}
		switch k {
		case kindInteger:
			if d.Min == nil || d.Max == nil || *d.Min != 1 || *d.Max != 2 {
				t.Errorf("%s: descriptor lacks its min and max", d.Kind)
			}
		case kindObject:
			if len(d.Fields) != 1 || d.Fields[0].Name != "a" || d.Fields[0].Kind != "boolean" {
				t.Errorf("%s: descriptor lacks its fields: %+v", d.Kind, d.Fields)
			}
		case kindCurve:
			if d.CurveType == nil || *d.CurveType != 7 {
				t.Errorf("%s: descriptor lacks its curveType", d.Kind)
			}
		}
	}
}

func TestDescriptorPerKind(t *testing.T) {
	byName := map[string]FunctionInfo{}
	for _, f := range Catalog() {
		byName[f.Name] = f
	}
	got := func(name string) string {
		b, err := json.Marshal(byName[name])
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	want := map[string]string{
		FuncConnect:        `{"name":"opModConnect","value":"boolean","description":"Connect (true) or disconnect (false) the DER.","kind":"boolean"}`,
		FuncMaxLimW:        `{"name":"opModMaxLimW","value":"integer 0 to 10000","description":"Active power ceiling in hundredths of a percent of setMaxW.","kind":"integer","min":0,"max":10000}`,
		FuncTargetW:        `{"name":"opModTargetW","value":"object {value int16, multiplier -9 to 9}","description":"Active power target, value x 10^multiplier watts, discharge positive.","kind":"object","fields":[{"name":"value","kind":"integer","required":true,"min":-32768,"max":32767},{"name":"multiplier","kind":"integer","min":-9,"max":9}]}`,
		FuncFixedPFInjectW: `{"name":"opModFixedPFInjectW","value":"object {displacement 1 to 1000, excitation boolean}","description":"Fixed power factor while injecting active power; displacement is thousandths.","kind":"object","fields":[{"name":"displacement","kind":"integer","required":true,"min":1,"max":1000},{"name":"excitation","kind":"boolean","required":true}]}`,
	}
	for name, w := range want {
		if g := got(name); g != w {
			t.Errorf("%s:\n got %s\nwant %s", name, g, w)
		}
	}
}

func TestCurveDescriptor(t *testing.T) {
	d := shape{kind: kindCurve, curveType: 7}.describe()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"kind":"curve","curveType":7}`; string(b) != want {
		t.Errorf("got %s, want %s", b, want)
	}
}

// intBound is one integer the catalog serves, with the request value that
// carries a candidate for it.
type intBound struct {
	where string
	min   int64
	max   int64
	body  func(v int64) string
}

func integerBounds(t *testing.T) (out []intBound) {
	t.Helper()
	for _, f := range Catalog() {
		name := f.Name
		switch f.Kind {
		case "integer":
			out = append(out, intBound{name, *f.Min, *f.Max, func(v int64) string { return fmt.Sprint(v) }})
		case "object":
			for i, m := range f.Fields {
				if m.Kind != "integer" {
					continue
				}
				out = append(out, intBound{name + "." + m.Name, *m.Min, *m.Max, func(v int64) string {
					parts := ""
					for j, o := range f.Fields {
						val := "true"
						if o.Kind == "integer" {
							val = fmt.Sprint(*o.Min)
						}
						if j == i {
							val = fmt.Sprint(v)
						}
						if j > 0 {
							parts += ","
						}
						parts += fmt.Sprintf("%q:%s", o.Name, val)
					}
					return "{" + parts + "}"
				}})
			}
		}
	}
	return out
}

// Each integer bound the descriptor serves is the bound decode enforces:
// min and max pass, and one past each is refused.
func TestServedIntegerBoundsAreEnforced(t *testing.T) {
	bounds := integerBounds(t)
	// maxLimW, both displacements, targetW and targetVar value and
	// multiplier, fixedVar refType and value, fixedW, the five droop
	// members and rampTms.
	if len(bounds) != 16 {
		t.Fatalf("served %d integer bounds, want 16", len(bounds))
	}
	decodes := func(b intBound, v int64) bool {
		name, _, _ := strings.Cut(b.where, ".")
		_, ref := DecodeFunctions(map[string]json.RawMessage{name: json.RawMessage(b.body(v))})
		return ref == nil
	}
	for _, b := range bounds {
		if !decodes(b, b.min) {
			t.Errorf("%s: min %d refused", b.where, b.min)
		}
		if !decodes(b, b.max) {
			t.Errorf("%s: max %d refused", b.where, b.max)
		}
		if b.min > math.MinInt64 && decodes(b, b.min-1) {
			t.Errorf("%s: %d accepted, one below min", b.where, b.min-1)
		}
		if b.max < math.MaxInt64 && decodes(b, b.max+1) {
			t.Errorf("%s: %d accepted, one above max", b.where, b.max+1)
		}
	}
}

// Each served `required` is what decode enforces: leaving out a required
// field is refused, and leaving out an optional one is accepted.
func TestServedRequiredMatchesDecode(t *testing.T) {
	checked := 0
	for _, f := range Catalog() {
		if f.Kind != "object" {
			continue
		}
		for _, omit := range f.Fields {
			var parts []string
			for _, o := range f.Fields {
				if o.Name == omit.Name {
					continue
				}
				val := "true"
				if o.Kind == "integer" {
					val = fmt.Sprint(*o.Min)
				}
				parts = append(parts, fmt.Sprintf("%q:%s", o.Name, val))
			}
			raw := json.RawMessage("{" + strings.Join(parts, ",") + "}")
			_, ref := DecodeFunctions(map[string]json.RawMessage{f.Name: raw})
			if refused := ref != nil; refused != omit.Required {
				t.Errorf("%s.%s: served required=%v, decode refused omission=%v", f.Name, omit.Name, omit.Required, refused)
			}
			checked++
		}
	}
	if checked != 15 {
		t.Errorf("checked %d object fields, want 15", checked)
	}
}
