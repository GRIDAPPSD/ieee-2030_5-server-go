package dercontrol

import (
	"context"
	"strings"
	"testing"
)

// The operator's description reaches the stored control's own description
// element, and a description past IEEE 2030.5's String32 bound is refused
// before anything is written.
func TestIssue_Description(t *testing.T) {
	cases := []struct {
		name        string
		description string
		wantRefusal RefusalCode
	}{
		{name: "empty stays empty", description: ""},
		{name: "32 characters stored", description: strings.Repeat("a", 32)},
		{name: "32 multi-byte characters stored", description: strings.Repeat("\u00e9", 32)},
		{name: "33 characters refused", description: strings.Repeat("a", 33), wantRefusal: RefusalInvalidDescription},
		{name: "invalid UTF-8 refused", description: "ok\xff", wantRefusal: RefusalInvalidDescription},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, Config{PEN: testPEN(1)})
			h.seedProgram(t, "dev1", "p1", controlListHref("dev1", "0", "p1"))
			res, err := h.issuer.Issue(context.Background(), CreateRequest{
				DERProgramHref:  programHref("dev1", "0", "p1"),
				Type:            Connect,
				DurationSeconds: 3600,
				Description:     tc.description,
			})
			if tc.wantRefusal != "" {
				assertRefusal(t, err, tc.wantRefusal)
				assertNoNewControl(t, h)
				return
			}
			if err != nil {
				t.Fatalf("Issue() error = %v", err)
			}
			got, err := h.controls.Get(context.Background(), "dev1/0/p1", res.ID)
			if err != nil {
				t.Fatalf("stored control: %v", err)
			}
			if got.Description != tc.description {
				t.Fatalf("stored Description = %q, want %q", got.Description, tc.description)
			}
			if res.Control.Description != tc.description {
				t.Fatalf("Result Description = %q, want %q", res.Control.Description, tc.description)
			}
		})
	}
}

func TestScopeKeyRoundTrip(t *testing.T) {
	s := Scope{EndDeviceID: "3", FSAID: "1", DERProgramID: "7"}
	if got := s.Key(); got != "3/1/7" {
		t.Fatalf("Key() = %q, want %q", got, "3/1/7")
	}
	back, ok := ScopeFromKey(s.Key())
	if !ok || back != s {
		t.Fatalf("ScopeFromKey(%q) = %+v, %v; want %+v, true", s.Key(), back, ok, s)
	}
	if got := s.ProgramHref(); got != "/edev/3/fsa/1/derp/7" {
		t.Fatalf("ProgramHref() = %q", got)
	}
	if got := s.ProgramListHref(); got != "/edev/3/fsa/1/derp" {
		t.Fatalf("ProgramListHref() = %q", got)
	}
	if got := s.ControlListHref(); got != "/edev/3/fsa/1/derp/7/derc" {
		t.Fatalf("ControlListHref() = %q", got)
	}
	for _, bad := range []string{"", "3/1", "3/1/7/9", "3//7", "/1/7"} {
		if _, ok := ScopeFromKey(bad); ok {
			t.Errorf("ScopeFromKey(%q) ok = true, want false", bad)
		}
	}
}
