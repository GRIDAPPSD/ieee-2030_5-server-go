package sep2admin

import (
	"context"
	"errors"
	"regexp"
	"unicode/utf8"
)

// Picker lets a Panel offer a closed list of choices from which the shell
// asks it to show a subset. View still answers when nothing is selected.
type Picker struct {
	// Choices lists what can be picked. It runs on every request that
	// needs it, under the same guard as View.
	Choices func(ctx context.Context) ([]Choice, error)

	// Select renders the panel for a selection. It receives only IDs the
	// plane matched against Choices, never a request value.
	Select func(ctx context.Context, s Selection) (Descriptor, error)
}

// Choice is one pickable item. ID is the stable key a selection carries;
// Label is the text the shell shows.
type Choice struct{ ID, Label string }

// Selection is the matched, validated IDs a request chose, in request
// order. Only NewSelection builds a non-empty one.
type Selection struct{ ids []string }

// IDs returns a copy of the selected IDs.
func (s Selection) IDs() []string { return append([]string(nil), s.ids...) }

// Len is the number of selected IDs.
func (s Selection) Len() int { return len(s.ids) }

// Selection and Choices bounds. MaxSelection equals MaxChartSeries so a
// selection always fits one chart.
const (
	MaxSelection   = MaxChartSeries
	MaxChoices     = 256
	MaxChoiceLabel = 128
)

// Sentinels for a refused selection and a refused choice list. Their text
// never carries a requested or offered value.
var (
	ErrInvalidSelection = errors.New("sep2admin: invalid selection")
	ErrInvalidChoices   = errors.New("sep2admin: invalid choices")

	// ErrInvalidPicker is returned by Register when a Picker is set with a
	// nil Choices or Select, a first-request panic otherwise.
	ErrInvalidPicker = errors.New("sep2admin: panel Picker needs both Choices and Select")
)

var selectionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// ValidateSelectionIDs refuses more than MaxSelection IDs, an ID outside
// the pattern, and a duplicate. It does not know the choices, so it
// cannot refuse an unknown ID.
func ValidateSelectionIDs(ids []string) error {
	if len(ids) > MaxSelection {
		return ErrInvalidSelection
	}
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if !selectionIDPattern.MatchString(id) {
			return ErrInvalidSelection
		}
		if _, dup := seen[id]; dup {
			return ErrInvalidSelection
		}
		seen[id] = struct{}{}
	}
	return nil
}

// ValidateChoices refuses more than MaxChoices entries, an ID outside the
// selection pattern, a label of 0 or more than MaxChoiceLabel characters
// or that is not valid UTF-8, and a duplicate ID or label.
func ValidateChoices(choices []Choice) error {
	if len(choices) > MaxChoices {
		return ErrInvalidChoices
	}
	ids := make(map[string]struct{}, len(choices))
	labels := make(map[string]struct{}, len(choices))
	for _, c := range choices {
		if !selectionIDPattern.MatchString(c.ID) || !utf8.ValidString(c.Label) {
			return ErrInvalidChoices
		}
		if n := utf8.RuneCountInString(c.Label); n < 1 || n > MaxChoiceLabel {
			return ErrInvalidChoices
		}
		if _, dup := ids[c.ID]; dup {
			return ErrInvalidChoices
		}
		if _, dup := labels[c.Label]; dup {
			return ErrInvalidChoices
		}
		ids[c.ID] = struct{}{}
		labels[c.Label] = struct{}{}
	}
	return nil
}

// NewSelection validates choices and requested, then keeps the requested
// IDs that appear in choices. A well-formed ID that is not a choice is
// dropped: a saved selection outlives a departed device. The result is
// built only from validated choices, so a caller cannot mint a Selection
// holding an ID that is not one of them.
func NewSelection(requested []string, choices []Choice) (Selection, error) {
	if err := ValidateChoices(choices); err != nil {
		return Selection{}, err
	}
	if err := ValidateSelectionIDs(requested); err != nil {
		return Selection{}, err
	}
	known := make(map[string]struct{}, len(choices))
	for _, c := range choices {
		known[c.ID] = struct{}{}
	}
	var kept []string
	for _, id := range requested {
		if _, ok := known[id]; ok {
			kept = append(kept, id)
		}
	}
	return Selection{ids: kept}, nil
}
