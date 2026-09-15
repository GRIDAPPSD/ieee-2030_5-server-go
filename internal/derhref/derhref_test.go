package derhref

import "testing"

func TestProgram(t *testing.T) {
	cases := []struct {
		name     string
		href     string
		wantEdev string
		wantFSA  string
		wantDerp string
		wantOK   bool
	}{
		{
			name:     "valid",
			href:     "/edev/dev1/fsa/0/derp/p1",
			wantEdev: "dev1",
			wantFSA:  "0",
			wantDerp: "p1",
			wantOK:   true,
		},
		{
			name:   "wrong literal in seg0",
			href:   "/xdev/dev1/fsa/0/derp/p1",
			wantOK: false,
		},
		{
			name:   "wrong literal in seg1",
			href:   "/edev/dev1/xsa/0/derp/p1",
			wantOK: false,
		},
		{
			name:   "wrong literal in seg2",
			href:   "/edev/dev1/fsa/0/xerp/p1",
			wantOK: false,
		},
		{
			name:   "empty edev value",
			href:   "/edev//fsa/0/derp/p1",
			wantOK: false,
		},
		{
			name:   "empty fsa value",
			href:   "/edev/dev1/fsa//derp/p1",
			wantOK: false,
		},
		{
			name:   "empty derp value",
			href:   "/edev/dev1/fsa/0/derp/",
			wantOK: false,
		},
		{
			name:   "too few segments",
			href:   "/edev/dev1/fsa/0",
			wantOK: false,
		},
		{
			name:   "too many segments",
			href:   "/edev/dev1/fsa/0/derp/p1/derc",
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edev, fsa, derp, ok := Program(c.href)
			if ok != c.wantOK {
				t.Fatalf("Program(%q) ok = %v, want %v", c.href, ok, c.wantOK)
			}
			if !c.wantOK {
				return
			}
			if edev != c.wantEdev || fsa != c.wantFSA || derp != c.wantDerp {
				t.Fatalf("Program(%q) = (%q, %q, %q), want (%q, %q, %q)", c.href, edev, fsa, derp, c.wantEdev, c.wantFSA, c.wantDerp)
			}
		})
	}
}
