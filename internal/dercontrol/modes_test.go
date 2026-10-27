package dercontrol

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
)

func TestModesOf(t *testing.T) {
	cases := []struct {
		name     string
		raw      uint32
		want     []string
		wantRes  uint32
		wantJSON string
	}{
		{"none", 0, []string{}, 0, `"names":[]`},
		{"bit 0", 1 << 0, []string{"Charge mode"}, 0, ""},
		{"bit 1", 1 << 1, []string{"Discharge mode"}, 0, ""},
		{"bit 2", 1 << 2, []string{FuncConnect}, 0, ""},
		{"bit 20", 1 << 20, []string{FuncMaxLimW}, 0, ""},
		{"bit 26, the last defined", 1 << 26, []string{FuncWattVar}, 0, ""},
		{"bit 27 is reserved", 1 << 27, []string{}, 1 << 27, ""},
		{"bit 31 is reserved", 1 << 31, []string{}, 1 << 31, ""},
		{"mixed keeps bit order", 1<<26 | 1<<3 | 1<<0 | 1<<30, []string{"Charge mode", FuncEnergize, FuncWattVar}, 1 << 30, ""},
	}
	for _, c := range cases {
		m := ModesOf(sep2.DERControlType(c.raw))
		if m.Raw != c.raw || !reflect.DeepEqual(m.Names, c.want) || m.ReservedBits != c.wantRes {
			t.Errorf("%s: got %+v, want names=%v reserved=%#x", c.name, m, c.want, c.wantRes)
		}
		if c.wantJSON != "" {
			b, _ := json.Marshal(m)
			if !contains(string(b), c.wantJSON) {
				t.Errorf("%s: json %s lacks %s", c.name, b, c.wantJSON)
			}
		}
	}
}

// Every element the standard names for bits 2 to 26 is a catalog function,
// so the bit table names nothing the catalog does not own.
func TestModeBitsNameCatalogFunctions(t *testing.T) {
	if len(modeBits) != 27 {
		t.Fatalf("modeBits has %d entries, want 27", len(modeBits))
	}
	known := map[string]bool{}
	for _, row := range catalog {
		known[row.info().Name] = true
	}
	seen := map[string]bool{}
	for i, name := range modeBits[2:] {
		if !known[name] {
			t.Errorf("bit %d names %q, which is not a catalog function", i+2, name)
		}
		if seen[name] {
			t.Errorf("bit %d repeats %q", i+2, name)
		}
		seen[name] = true
	}
	// Every catalog function but rampTms, which is not a mode, has a bit.
	for name := range known {
		if name != FuncRampTms && !seen[name] {
			t.Errorf("catalog function %q has no DERControlType bit", name)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
