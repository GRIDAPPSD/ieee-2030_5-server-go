package sep2admin

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestValidateSelectionIDs(t *testing.T) {
	seventeen := make([]string, 17)
	for i := range seventeen {
		seventeen[i] = "id" + string(rune('a'+i))
	}
	cases := []struct {
		name string
		ids  []string
		ok   bool
	}{
		{"none", nil, true},
		{"pattern edge characters", []string{"A_b.c:d-9"}, true},
		{"sixteen is the cap", seventeen[:16], true},
		{"seventeen", seventeen, false},
		{"64 characters", []string{strings.Repeat("a", 64)}, true},
		{"65 characters", []string{strings.Repeat("a", 65)}, false},
		{"empty", []string{""}, false},
		{"space", []string{"a b"}, false},
		{"angle bracket", []string{"<b>"}, false},
		{"slash", []string{"a/b"}, false},
		{"duplicate", []string{"a", "b", "a"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSelectionIDs(tc.ids)
			if tc.ok && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidSelection) {
				t.Fatalf("err = %v, want ErrInvalidSelection", err)
			}
		})
	}
}

func TestValidateChoices(t *testing.T) {
	many := make([]Choice, MaxChoices+1)
	for i := range many {
		id := "c" + strings.Repeat("x", i/26) + string(rune('a'+i%26))
		many[i] = Choice{ID: id, Label: "L" + id}
	}
	cases := []struct {
		name string
		in   []Choice
		ok   bool
	}{
		{"empty", nil, true},
		{"at the cap", many[:MaxChoices], true},
		{"over the cap", many, false},
		{"bad id", []Choice{{"a b", "A"}}, false},
		{"empty label", []Choice{{"a", ""}}, false},
		{"128 character label", []Choice{{"a", strings.Repeat("e", 128)}}, true},
		{"129 character label", []Choice{{"a", strings.Repeat("e", 129)}}, false},
		{"multibyte label counts characters", []Choice{{"a", strings.Repeat("\u00e9", 128)}}, true},
		{"invalid utf-8 label", []Choice{{"a", "\xff"}}, false},
		{"duplicate id", []Choice{{"a", "A"}, {"a", "B"}}, false},
		{"duplicate label", []Choice{{"a", "A"}, {"b", "A"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateChoices(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidChoices) {
				t.Fatalf("err = %v, want ErrInvalidChoices", err)
			}
		})
	}
}

func TestNewSelectionKeepsOnlyMatchedIDsInRequestOrder(t *testing.T) {
	choices := []Choice{{"a", "A"}, {"b", "B"}, {"c", "C"}}
	sel, err := NewSelection([]string{"c", "gone", "a"}, choices)
	if err != nil {
		t.Fatalf("NewSelection: %v", err)
	}
	if got, want := sel.IDs(), []string{"c", "a"}; !slices.Equal(got, want) {
		t.Fatalf("IDs = %v, want %v", got, want)
	}
	if sel.Len() != 2 {
		t.Fatalf("Len = %d, want 2", sel.Len())
	}

	// IDs returns a copy: editing it must not change the selection.
	sel.IDs()[0] = "zzz"
	if got := sel.IDs()[0]; got != "c" {
		t.Fatalf("IDs()[0] = %q after an edit of a returned slice, want c", got)
	}
}

func TestNewSelectionAllUnknownIsEmpty(t *testing.T) {
	sel, err := NewSelection([]string{"x", "y"}, []Choice{{"a", "A"}})
	if err != nil || sel.Len() != 0 {
		t.Fatalf("got %v, %v; want an empty selection and no error", sel, err)
	}
}

func TestNewSelectionRefusesWhatItCannotTrust(t *testing.T) {
	if _, err := NewSelection([]string{"a", "a"}, []Choice{{"a", "A"}}); !errors.Is(err, ErrInvalidSelection) {
		t.Errorf("duplicate request: err = %v, want ErrInvalidSelection", err)
	}
	if _, err := NewSelection([]string{"a"}, []Choice{{"a", "A"}, {"a", "B"}}); !errors.Is(err, ErrInvalidChoices) {
		t.Errorf("duplicate choice: err = %v, want ErrInvalidChoices", err)
	}
	if _, err := NewSelection([]string{"bad id"}, nil); !errors.Is(err, ErrInvalidSelection) {
		t.Errorf("bad id: err = %v, want ErrInvalidSelection", err)
	}
}

func TestRegisterRefusesAPickerMissingAFunc(t *testing.T) {
	choices := func(context.Context) ([]Choice, error) { return nil, nil }
	sel := func(context.Context, Selection) (Descriptor, error) { return Descriptor{}, nil }
	cases := map[string]*Picker{
		"nil Choices": {Select: sel},
		"nil Select":  {Choices: choices},
		"both nil":    {},
	}
	for name, pk := range cases {
		t.Run(name, func(t *testing.T) {
			p := graftPanel("picker-panel", 1)
			p.Picker = pk
			if err := NewRegistry().Register(p); !errors.Is(err, ErrInvalidPicker) {
				t.Fatalf("Register: err = %v, want ErrInvalidPicker", err)
			}
		})
	}
	p := graftPanel("picker-panel", 1)
	p.Picker = &Picker{Choices: choices, Select: sel}
	if err := NewRegistry().Register(p); err != nil {
		t.Fatalf("Register with both funcs: %v", err)
	}
}
