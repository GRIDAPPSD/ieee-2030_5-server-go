package sep2admin

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func okRun(context.Context, ActionValues) (ActionResult, error) { return ActionResult{}, nil }

func devices(context.Context) ([]Choice, error) {
	return []Choice{{ID: "dev-1", Label: "Inverter 1"}, {ID: "dev-2", Label: "Inverter 2"}}, nil
}

// formAction has one field of every kind.
func formAction() Action {
	return Action{
		ID:    "set-target",
		Label: "Set target",
		Fields: []ActionField{
			{Name: "device", Label: "Device", Kind: ActionChoice, Choices: devices},
			{Name: "multiplier", Label: "Multiplier", Kind: ActionInteger, Min: -9, Max: 9},
			{Name: "connect", Label: "Connect", Kind: ActionBoolean},
			{Name: "publishing", Label: "Publishing", Kind: ActionToggle},
			{Name: "raw", Label: "Raw", Kind: ActionText, MaxLen: 32},
		},
		Run: okRun,
	}
}

func TestParseReturnsTheSubmittedValues(t *testing.T) {
	v, err := formAction().Parse([]byte(`{"device":"dev-2","multiplier":-9,"connect":true,"publishing":false,"raw":"{\"a\":1}\nline"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := v.String("device"); got != "dev-2" {
		t.Errorf("device = %q, want dev-2", got)
	}
	if got := v.Int("multiplier"); got != -9 {
		t.Errorf("multiplier = %d, want -9", got)
	}
	if !v.Bool("connect") || v.Bool("publishing") {
		t.Errorf("connect, publishing = %t, %t; want true, false", v.Bool("connect"), v.Bool("publishing"))
	}
	if got, want := v.String("raw"), "{\"a\":1}\nline"; got != want {
		t.Errorf("raw = %q, want %q", got, want)
	}
}

func TestParseRefusesWhatTheSchemaForbids(t *testing.T) {
	const ok = `"device":"dev-1","multiplier":0,"connect":true,"publishing":true,"raw":"x"`
	cases := []struct {
		name, body, field string
	}{
		{"integer over max", `{"device":"dev-1","multiplier":10,"connect":true,"publishing":true,"raw":"x"}`, "multiplier"},
		{"integer under min", `{"device":"dev-1","multiplier":-10,"connect":true,"publishing":true,"raw":"x"}`, "multiplier"},
		{"fraction", `{"device":"dev-1","multiplier":1.5,"connect":true,"publishing":true,"raw":"x"}`, "multiplier"},
		{"whole number written as float", `{"device":"dev-1","multiplier":1.0,"connect":true,"publishing":true,"raw":"x"}`, "multiplier"},
		{"exponent", `{"device":"dev-1","multiplier":1e0,"connect":true,"publishing":true,"raw":"x"}`, "multiplier"},
		{"quoted integer", `{"device":"dev-1","multiplier":"1","connect":true,"publishing":true,"raw":"x"}`, "multiplier"},
		{"string for boolean", `{"device":"dev-1","multiplier":0,"connect":"true","publishing":true,"raw":"x"}`, "connect"},
		{"number for toggle", `{"device":"dev-1","multiplier":0,"connect":true,"publishing":1,"raw":"x"}`, "publishing"},
		{"null boolean", `{"device":"dev-1","multiplier":0,"connect":null,"publishing":true,"raw":"x"}`, "connect"},
		{"choice id with a space", `{"device":"dev 1","multiplier":0,"connect":true,"publishing":true,"raw":"x"}`, "device"},
		{"number for choice", `{"device":1,"multiplier":0,"connect":true,"publishing":true,"raw":"x"}`, "device"},
		{"empty text", `{"device":"dev-1","multiplier":0,"connect":true,"publishing":true,"raw":""}`, "raw"},
		{"text over max", `{"device":"dev-1","multiplier":0,"connect":true,"publishing":true,"raw":"` + strings.Repeat("a", 33) + `"}`, "raw"},
		{"text with NUL", `{"device":"dev-1","multiplier":0,"connect":true,"publishing":true,"raw":"a\u0000b"}`, "raw"},
		{"text with escape", `{"device":"dev-1","multiplier":0,"connect":true,"publishing":true,"raw":"a\u001bb"}`, "raw"},
		{"missing field", `{"device":"dev-1","multiplier":0,"connect":true,"publishing":true,"extra":"x"}`, "raw"},
		{"unknown field", `{` + ok + `,"extra":1}`, ""},
		{"duplicate key", `{` + ok + `,"multiplier":1}`, ""},
		{"trailing data", `{` + ok + `} {}`, ""},
		{"array", `[1]`, ""},
		{"not json", `device=dev-1`, ""},
		{"empty body", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := formAction().Parse([]byte(c.body))
			if !errors.Is(err, ErrInvalidActionValues) {
				t.Fatalf("Parse(%s) = %v, want ErrInvalidActionValues", c.body, err)
			}
			var ve *ActionValueError
			if !errors.As(err, &ve) || ve.Field != c.field {
				t.Fatalf("refusal field = %+v, want %q", ve, c.field)
			}
			if strings.Contains(err.Error(), "dev") {
				t.Errorf("error text %q carries a submitted value", err)
			}
		})
	}
}

func TestParseAcceptsTheIntegerBounds(t *testing.T) {
	a := Action{ID: "a", Label: "A", Run: okRun, Fields: []ActionField{{Name: "v", Label: "V", Kind: ActionInteger, Min: -32768, Max: 32767}}}
	for body, want := range map[string]int64{`{"v":-32768}`: -32768, `{"v":32767}`: 32767, `{"v":0}`: 0} {
		v, err := a.Parse([]byte(body))
		if err != nil || v.Int("v") != want {
			t.Errorf("Parse(%s) = %d, %v; want %d", body, v.Int("v"), err, want)
		}
	}
	for _, body := range []string{`{"v":-32769}`, `{"v":32768}`} {
		if _, err := a.Parse([]byte(body)); !errors.Is(err, ErrInvalidActionValues) {
			t.Errorf("Parse(%s) = %v, want a refusal", body, err)
		}
	}
}

func TestCheckChoicesRefusesAnIDOutsideTheList(t *testing.T) {
	a := formAction()
	good, err := a.Parse([]byte(`{"device":"dev-1","multiplier":0,"connect":true,"publishing":true,"raw":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CheckChoices(context.Background(), good); err != nil {
		t.Fatalf("CheckChoices(dev-1) = %v, want nil", err)
	}
	gone, err := a.Parse([]byte(`{"device":"dev-9","multiplier":0,"connect":true,"publishing":true,"raw":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	var ve *ActionValueError
	if err := a.CheckChoices(context.Background(), gone); !errors.As(err, &ve) || ve.Field != "device" {
		t.Fatalf("CheckChoices(dev-9) = %v, want a refusal on device", err)
	}
}

func TestCheckChoicesDoesNotTurnAnEmbedderErrorIntoARefusal(t *testing.T) {
	boom := errors.New("registry down")
	a := Action{ID: "a", Label: "A", Run: okRun, Fields: []ActionField{
		{Name: "d", Label: "D", Kind: ActionChoice, Choices: func(context.Context) ([]Choice, error) { return nil, boom }},
	}}
	v, err := a.Parse([]byte(`{"d":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.CheckChoices(context.Background(), v); !errors.Is(err, boom) || errors.Is(err, ErrInvalidActionValues) {
		t.Fatalf("CheckChoices = %v, want the embedder's own error", err)
	}
}

func TestCheckChoicesRefusesAnInvalidList(t *testing.T) {
	a := Action{ID: "a", Label: "A", Run: okRun, Fields: []ActionField{
		{Name: "d", Label: "D", Kind: ActionChoice, Choices: func(context.Context) ([]Choice, error) {
			return []Choice{{ID: "has space", Label: "x"}}, nil
		}},
	}}
	v, _ := a.Parse([]byte(`{"d":"x"}`))
	if err := a.CheckChoices(context.Background(), v); !errors.Is(err, ErrInvalidChoices) {
		t.Fatalf("CheckChoices = %v, want ErrInvalidChoices", err)
	}
}

func TestRegisterRefusesAnInvalidAction(t *testing.T) {
	tooMany := make([]Action, MaxActionsPerPanel+1)
	for i := range tooMany {
		tooMany[i] = Action{ID: "a" + strings.Repeat("x", i), Label: "A", Run: okRun, Fields: []ActionField{{Name: "b", Label: "B", Kind: ActionBoolean}}}
	}
	bad := func(mut func(*Action)) []Action {
		a := formAction()
		mut(&a)
		return []Action{a}
	}
	cases := map[string][]Action{
		"upper-case id":      bad(func(a *Action) { a.ID = "Set" }),
		"nil Run":            bad(func(a *Action) { a.Run = nil }),
		"empty label":        bad(func(a *Action) { a.Label = "" }),
		"no fields":          bad(func(a *Action) { a.Fields = nil }),
		"too many fields":    bad(func(a *Action) { a.Fields = append(a.Fields, a.Fields...) }),
		"duplicate field":    bad(func(a *Action) { a.Fields[1].Name = "device" }),
		"bad field name":     bad(func(a *Action) { a.Fields[0].Name = "my field" }),
		"unknown kind":       bad(func(a *Action) { a.Fields[0].Kind = "slider" }),
		"choice without fn":  bad(func(a *Action) { a.Fields[0].Choices = nil }),
		"inverted range":     bad(func(a *Action) { a.Fields[1].Min, a.Fields[1].Max = 5, -5 }),
		"text without max":   bad(func(a *Action) { a.Fields[4].MaxLen = 0 }),
		"text over cap":      bad(func(a *Action) { a.Fields[4].MaxLen = MaxActionTextLen + 1 }),
		"duplicate action":   {formAction(), formAction()},
		"too many actions":   tooMany,
		"non-UTF-8 label":    bad(func(a *Action) { a.Label = "\xff" }),
		"label over maximum": bad(func(a *Action) { a.Label = strings.Repeat("a", MaxActionLabel+1) }),
	}
	for name, actions := range cases {
		t.Run(name, func(t *testing.T) {
			r := NewRegistry()
			err := r.Register(Panel{ID: "p", Label: "P", Placement: ExtensionSlot(1), DescriptorVersion: CurrentDescriptorVersion, View: nilView, Actions: actions})
			if !errors.Is(err, ErrInvalidAction) {
				t.Fatalf("Register = %v, want ErrInvalidAction", err)
			}
		})
	}

	r := NewRegistry()
	p := Panel{ID: "p", Label: "P", Placement: ExtensionSlot(1), DescriptorVersion: CurrentDescriptorVersion, View: nilView, Actions: []Action{formAction()}}
	if err := r.Register(p); err != nil {
		t.Fatalf("control: Register of a valid Action = %v, want nil", err)
	}
}

func nilView(context.Context) (Descriptor, error) {
	return Descriptor{Version: CurrentDescriptorVersion}, nil
}
