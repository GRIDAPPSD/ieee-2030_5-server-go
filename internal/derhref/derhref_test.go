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

func TestControlID(t *testing.T) {
	cases := []struct {
		name   string
		href   string
		wantID string
		wantOK bool
	}{
		{name: "valid", href: "/edev/dev1/fsa/0/derp/p1/derc/abc123", wantID: "abc123", wantOK: true},
		{name: "no marker", href: "/edev/dev1/fsa/0/derp/p1", wantOK: false},
		{name: "empty id", href: "/edev/dev1/fsa/0/derp/p1/derc/", wantOK: false},
		{name: "program href, not a control href", href: "/edev/dev1/fsa/0/derp/p1", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, ok := ControlID(c.href)
			if ok != c.wantOK {
				t.Fatalf("ControlID(%q) ok = %v, want %v", c.href, ok, c.wantOK)
			}
			if c.wantOK && id != c.wantID {
				t.Fatalf("ControlID(%q) = %q, want %q", c.href, id, c.wantID)
			}
		})
	}
}

func TestControlList(t *testing.T) {
	cases := []struct {
		name     string
		href     string
		wantEdev string
		wantFSA  string
		wantDerp string
		wantOK   bool
	}{
		{name: "valid", href: "/edev/dev1/fsa/0/derp/p1/derc", wantEdev: "dev1", wantFSA: "0", wantDerp: "p1", wantOK: true},
		{name: "missing derc suffix (the program's own href)", href: "/edev/dev1/fsa/0/derp/p1", wantOK: false},
		{name: "malformed program segment", href: "/edev/dev1/xsa/0/derp/p1/derc", wantOK: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edev, fsa, derp, ok := ControlList(c.href)
			if ok != c.wantOK {
				t.Fatalf("ControlList(%q) ok = %v, want %v", c.href, ok, c.wantOK)
			}
			if !c.wantOK {
				return
			}
			if edev != c.wantEdev || fsa != c.wantFSA || derp != c.wantDerp {
				t.Fatalf("ControlList(%q) = (%q, %q, %q), want (%q, %q, %q)", c.href, edev, fsa, derp, c.wantEdev, c.wantFSA, c.wantDerp)
			}
		})
	}
}

func TestControlListScope(t *testing.T) {
	key, ok := ControlListScope("/edev/dev1/fsa/0/derp/p1/derc")
	if !ok {
		t.Fatal("ControlListScope: ok = false, want true")
	}
	if want := "dev1/0/p1"; key != want {
		t.Fatalf("ControlListScope = %q, want %q", key, want)
	}

	if _, ok := ControlListScope("/edev/dev1/fsa/0/derp/p1"); ok {
		t.Fatal("ControlListScope on a program href (no /derc suffix): ok = true, want false")
	}
}
