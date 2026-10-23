package mrid

import "testing"

func TestCanonical(t *testing.T) {
	const up = "0123456789ABCDEF0123456789ABCDEF"
	cases := []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"32 upper", up, up, true},
		{"32 lower", "0123456789abcdef0123456789abcdef", up, true},
		{"32 mixed", "0123456789aBcDeF0123456789AbCdEf", up, true},
		{"31 digits", up[:31], "", false},
		{"33 digits", up + "0", "", false},
		{"empty", "", "", false},
		{"non hex at end", up[:31] + "G", "", false},
		{"non hex at start", "g" + up[1:], "", false},
		{"padded with space", " " + up[:31], "", false},
	}
	for _, c := range cases {
		got, ok := Canonical(c.in)
		if got != c.want || ok != c.wantOK {
			t.Errorf("%s: Canonical(%q) = (%q, %v), want (%q, %v)", c.name, c.in, got, ok, c.want, c.wantOK)
		}
	}
}
