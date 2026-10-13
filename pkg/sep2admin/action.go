package sep2admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"unicode/utf8"
)

// Action is a typed form a Panel offers its operator, served as
// POST /api/ui/panels/{id}/actions/{action} when the plane's PanelActions
// setting is on. The plane validates a submission against Fields before Run
// is called, so Run never sees a value outside the declared schema.
type Action struct {
	// ID is the action's slug, unique within its panel.
	ID string

	// Label is the text the shell shows on the form.
	Label string

	// Fields are the form's inputs, 1 to MaxActionFields. Every field is
	// required in a submission, and a submission may carry no other.
	Fields []ActionField

	// Run performs the action. It runs under the panel's one-at-a-time flag
	// and the plane's timeout, and ctx ends when the request does or the
	// plane is closed. A refusal the operator should read is returned as an
	// *ActionRefusal; any other error is logged and answered as a bare
	// failure.
	Run func(ctx context.Context, v ActionValues) (ActionResult, error)
}

// ActionFieldKind is how a field's value is declared on the wire and shown.
type ActionFieldKind string

const (
	// ActionChoice is a string equal to the ID of one of the field's Choices.
	ActionChoice ActionFieldKind = "choice"
	// ActionInteger is a JSON integer from Min to Max.
	ActionInteger ActionFieldKind = "integer"
	// ActionBoolean is a JSON true or false.
	ActionBoolean ActionFieldKind = "boolean"
	// ActionToggle is a JSON true or false the shell shows as a switch.
	ActionToggle ActionFieldKind = "toggle"
	// ActionText is a multiline string of 1 to MaxLen bytes.
	ActionText ActionFieldKind = "text"
)

// ActionField declares one input. Only the members of its Kind are read.
type ActionField struct {
	// Name keys the value in a submission and in ActionValues.
	Name string
	// Label is the text the shell shows beside the input.
	Label string
	Kind  ActionFieldKind

	// Choices lists the pickable items of an ActionChoice field. It runs
	// under the same guard as Run, on every submission and every schema
	// read, so a departed device is refused rather than trusted.
	Choices func(ctx context.Context) ([]Choice, error)

	// Min and Max bound an ActionInteger field, inclusive.
	Min, Max int64

	// MaxLen bounds an ActionText field in bytes, 1 to MaxActionTextLen.
	MaxLen int
}

// ActionResult is what a completed action tells the operator.
type ActionResult struct {
	// Message is shown as the outcome. The plane cuts it to
	// MaxActionMessageBytes and replaces invalid UTF-8.
	Message string
}

// ActionRefusal is the error Run returns for a refusal whose Reason the
// operator should read, such as "publishing is off". The plane answers it
// as 422 with the Reason, cut like ActionResult.Message.
type ActionRefusal struct{ Reason string }

func (e *ActionRefusal) Error() string { return "sep2admin: action refused: " + e.Reason }

// Action bounds. MaxActionBodyBytes caps the encoded body, so a text field
// holding quotes or control bytes, which JSON escapes, reaches it before
// MaxActionTextLen does.
const (
	MaxActionsPerPanel    = 8
	MaxActionFields       = 8
	MaxActionLabel        = 128
	MaxActionTextLen      = 16 << 10
	MaxActionBodyBytes    = 16 << 10
	MaxActionMessageBytes = 512
)

var (
	// ErrInvalidAction is returned by Register for an Action the plane
	// cannot serve: a bad ID or field name, a duplicate, a missing Run or
	// Choices, an inverted integer range, or a bound out of range.
	ErrInvalidAction = errors.New("sep2admin: invalid panel Action")

	// ErrInvalidActionValues refuses a submission. It is matched with
	// errors.Is; an *ActionValueError carries the field. Its text never
	// carries a submitted value.
	ErrInvalidActionValues = errors.New("sep2admin: invalid action values")
)

// ActionValueError is a refused submission. Field names the declared field
// the refusal is about, or is empty when the body as a whole is refused.
type ActionValueError struct{ Field string }

func (e *ActionValueError) Error() string { return ErrInvalidActionValues.Error() }

// Is makes errors.Is(err, ErrInvalidActionValues) true.
func (e *ActionValueError) Is(target error) bool { return target == ErrInvalidActionValues }

var (
	actionIDPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	actionFieldPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,31}$`)
)

func (a Action) validate() error {
	if !actionIDPattern.MatchString(a.ID) || a.Run == nil || !validActionLabel(a.Label) {
		return ErrInvalidAction
	}
	if n := len(a.Fields); n < 1 || n > MaxActionFields {
		return ErrInvalidAction
	}
	seen := make(map[string]struct{}, len(a.Fields))
	for _, f := range a.Fields {
		if !actionFieldPattern.MatchString(f.Name) || !validActionLabel(f.Label) {
			return ErrInvalidAction
		}
		if _, dup := seen[f.Name]; dup {
			return ErrInvalidAction
		}
		seen[f.Name] = struct{}{}
		if err := f.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (f ActionField) validate() error {
	switch f.Kind {
	case ActionChoice:
		if f.Choices == nil {
			return ErrInvalidAction
		}
	case ActionInteger:
		if f.Min > f.Max {
			return ErrInvalidAction
		}
	case ActionBoolean, ActionToggle:
	case ActionText:
		if f.MaxLen < 1 || f.MaxLen > MaxActionTextLen {
			return ErrInvalidAction
		}
	default:
		return ErrInvalidAction
	}
	return nil
}

func validActionLabel(s string) bool {
	n := utf8.RuneCountInString(s)
	return utf8.ValidString(s) && n >= 1 && n <= MaxActionLabel
}

// ActionValues are a validated submission. An accessor panics for a name
// the Action does not declare or declares as another kind, so a misspelled
// name in Run fails the action loudly (the plane answers 500 and logs the
// name) instead of reading as 0, false or "".
type ActionValues struct{ v map[string]any }

func (a ActionValues) get(name, kind string) any {
	v, ok := a.v[name]
	if !ok {
		panic(fmt.Sprintf("sep2admin: ActionValues.%s: no field %q", kind, name))
	}
	return v
}

// Int returns an ActionInteger field's value.
func (a ActionValues) Int(name string) int64 {
	n, ok := a.get(name, "Int").(int64)
	if !ok {
		panic(fmt.Sprintf("sep2admin: ActionValues.Int: field %q is not an integer", name))
	}
	return n
}

// Bool returns an ActionBoolean or ActionToggle field's value.
func (a ActionValues) Bool(name string) bool {
	b, ok := a.get(name, "Bool").(bool)
	if !ok {
		panic(fmt.Sprintf("sep2admin: ActionValues.Bool: field %q is not a boolean", name))
	}
	return b
}

// String returns an ActionChoice field's chosen ID or an ActionText
// field's text.
func (a ActionValues) String(name string) string {
	s, ok := a.get(name, "String").(string)
	if !ok {
		panic(fmt.Sprintf("sep2admin: ActionValues.String: field %q is not a choice or text", name))
	}
	return s
}

// Parse checks body against the declared fields, short of an ActionChoice
// field's membership, which needs the embedder (CheckChoices). Body must be
// one JSON object holding exactly the declared names, each once, with a
// value of the declared kind and range. It does not read the request.
func (a Action) Parse(body []byte) (ActionValues, error) {
	raw, err := decodeObject(body)
	if err != nil {
		return ActionValues{}, &ActionValueError{}
	}
	if len(raw) != len(a.Fields) {
		return ActionValues{}, &ActionValueError{}
	}
	out := make(map[string]any, len(a.Fields))
	for _, f := range a.Fields {
		lit, ok := raw[f.Name]
		if !ok {
			return ActionValues{}, &ActionValueError{Field: f.Name}
		}
		v, err := f.parse(lit)
		if err != nil {
			return ActionValues{}, &ActionValueError{Field: f.Name}
		}
		out[f.Name] = v
	}
	return ActionValues{v: out}, nil
}

// decodeObject reads one JSON object and nothing after it, refusing a
// duplicate key: encoding/json would keep the last silently.
func decodeObject(body []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, ErrInvalidActionValues
	}
	out := map[string]json.RawMessage{}
	for dec.More() {
		tok, err := dec.Token()
		key, ok := tok.(string)
		if err != nil || !ok {
			return nil, ErrInvalidActionValues
		}
		if _, dup := out[key]; dup {
			return nil, ErrInvalidActionValues
		}
		var lit json.RawMessage
		if err := dec.Decode(&lit); err != nil {
			return nil, ErrInvalidActionValues
		}
		out[key] = lit
	}
	if tok, err := dec.Token(); err != nil || tok != json.Delim('}') {
		return nil, ErrInvalidActionValues
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidActionValues
	}
	return out, nil
}

func (f ActionField) parse(lit json.RawMessage) (any, error) {
	switch f.Kind {
	case ActionInteger:
		// ParseInt on the literal refuses 1.0, 1e2 and a quoted number, which
		// a float64 round trip would accept.
		n, err := strconv.ParseInt(string(lit), 10, 64)
		if err != nil || n < f.Min || n > f.Max {
			return nil, ErrInvalidActionValues
		}
		return n, nil
	case ActionBoolean, ActionToggle:
		switch string(lit) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
		return nil, ErrInvalidActionValues
	case ActionChoice, ActionText:
		var s string
		if err := json.Unmarshal(lit, &s); err != nil {
			return nil, ErrInvalidActionValues
		}
		if f.Kind == ActionChoice {
			if !selectionIDPattern.MatchString(s) {
				return nil, ErrInvalidActionValues
			}
			return s, nil
		}
		if !validActionText(s, f.MaxLen) {
			return nil, ErrInvalidActionValues
		}
		return s, nil
	}
	return nil, ErrInvalidActionValues
}

// validActionText accepts 1 to maxLen bytes of valid UTF-8 with no control
// character but tab, newline and carriage return. It refuses the C0 set, DEL,
// the C1 set (U+009B is a CSI to a terminal) and the bidirectional
// embedding, override and isolate controls, which reorder text on screen: a
// text a Run forwards or an operator reads back would otherwise carry them.
func validActionText(s string, maxLen int) bool {
	if len(s) < 1 || len(s) > maxLen || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r < 0x20 && r != '\n' && r != '\r' && r != '\t', r >= 0x7F && r <= 0x9F,
			r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
			return false
		}
	}
	return true
}

// CheckChoices resolves every ActionChoice field's Choices, validates the
// list, and refuses a submitted ID that is not among them. An error from
// Choices itself is returned as is, not as an ActionValueError.
func (a Action) CheckChoices(ctx context.Context, v ActionValues) error {
	for _, f := range a.Fields {
		if f.Kind != ActionChoice {
			continue
		}
		choices, err := f.Choices(ctx)
		if err != nil {
			return err
		}
		if err := ValidateChoices(choices); err != nil {
			return err
		}
		want := v.String(f.Name)
		found := false
		for _, c := range choices {
			if c.ID == want {
				found = true
				break
			}
		}
		if !found {
			return &ActionValueError{Field: f.Name}
		}
	}
	return nil
}

// ListChoices resolves each ActionChoice field's validated Choices, keyed
// by field name, for the shell to render a form.
func (a Action) ListChoices(ctx context.Context) (map[string][]Choice, error) {
	out := map[string][]Choice{}
	for _, f := range a.Fields {
		if f.Kind != ActionChoice {
			continue
		}
		c, err := f.Choices(ctx)
		if err != nil {
			return nil, err
		}
		if err := ValidateChoices(c); err != nil {
			return nil, err
		}
		out[f.Name] = c
	}
	return out, nil
}

// validateActions is Register's check of a Panel's Actions.
func validateActions(actions []Action) error {
	if len(actions) > MaxActionsPerPanel {
		return ErrInvalidAction
	}
	seen := make(map[string]struct{}, len(actions))
	for _, a := range actions {
		if err := a.validate(); err != nil {
			return err
		}
		if _, dup := seen[a.ID]; dup {
			return ErrInvalidAction
		}
		seen[a.ID] = struct{}{}
	}
	return nil
}
