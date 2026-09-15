package server

import (
	"context"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// HasProgram now parses hrefs through the shared internal/derhref package
// (#563) instead of a locally duplicated split. This covers the resolve
// and malformed-href paths at the wiring's own consumer, keeping parity
// with the extraction.
func TestDERProgramHrefValidator_HasProgram(t *testing.T) {
	programs := memory.NewScopedStore[sep2.DERProgram]()
	if err := programs.Create(context.Background(), "dev1", "p1", sep2.DERProgram{}); err != nil {
		t.Fatalf("seed program: %v", err)
	}
	v := &derProgramHrefValidator{programs: programs}

	cases := []struct {
		name string
		href string
		want bool
	}{
		{"resolves", "/edev/dev1/fsa/0/derp/p1", true},
		{"unknown program", "/edev/dev1/fsa/0/derp/p2", false},
		{"malformed href", "/not/a/program/href", false},
		{"empty fsa segment", "/edev/dev1/fsa//derp/p1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := v.HasProgram(context.Background(), c.href); got != c.want {
				t.Fatalf("HasProgram(%q) = %v, want %v", c.href, got, c.want)
			}
		})
	}
}

func TestDERProgramHrefValidator_HasProgram_NilReceiverOrStore(t *testing.T) {
	var nilValidator *derProgramHrefValidator
	if nilValidator.HasProgram(context.Background(), "/edev/dev1/fsa/0/derp/p1") {
		t.Fatalf("HasProgram on nil validator = true, want false")
	}
	v := &derProgramHrefValidator{}
	if v.HasProgram(context.Background(), "/edev/dev1/fsa/0/derp/p1") {
		t.Fatalf("HasProgram with nil store = true, want false")
	}
}
