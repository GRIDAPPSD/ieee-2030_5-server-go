package adminplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2admin"
)

const refusalBody = `{"error":"invalid selection"}`

func headed(heading string) sep2admin.Descriptor {
	return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion, Sections: []sep2admin.Section{{
		Heading: heading,
		Body:    sep2admin.NewTableBody(sep2admin.TableBody{Columns: []string{"x"}}),
	}}}
}

// pickerFake is a picker panel whose three calls count themselves.
type pickerFake struct {
	choices                         []sep2admin.Choice
	choicesErr                      error
	choicesCalls, selects, defaults atomic.Int32
	mu                              sync.Mutex
	got                             []string
}

func (f *pickerFake) panel(id string) sep2admin.Panel {
	p := testPanel(id, 1, func(context.Context) (sep2admin.Descriptor, error) {
		f.defaults.Add(1)
		return headed("default"), nil
	})
	p.Picker = &sep2admin.Picker{
		Choices: func(context.Context) ([]sep2admin.Choice, error) {
			f.choicesCalls.Add(1)
			return f.choices, f.choicesErr
		},
		Select: func(_ context.Context, s sep2admin.Selection) (sep2admin.Descriptor, error) {
			f.selects.Add(1)
			f.mu.Lock()
			f.got = s.IDs()
			f.mu.Unlock()
			return headed("selected"), nil
		},
	}
	return p
}

func (f *pickerFake) selected() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got
}

func threeChoices() []sep2admin.Choice {
	return []sep2admin.Choice{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}, {ID: "c", Label: "C"}}
}

func TestPanelListCarriesPickerOnlyWhenDeclared(t *testing.T) {
	f := &pickerFake{choices: threeChoices()}
	h := buildWithPanels(t, f.panel("with-picker"), testPanel("plain", 2, okView))
	rec := get(h, "/api/ui/panels", true)
	want := `[{"id":"with-picker","label":"Label with-picker","picker":{"max":16}},{"id":"plain","label":"Label plain"}]`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("got %d %s, want 200 %s", rec.Code, rec.Body, want)
	}
}

func TestSelectReceivesOnlyMatchedIDs(t *testing.T) {
	f := &pickerFake{choices: threeChoices()}
	h := buildWithPanels(t, f.panel("p"))
	rec := get(h, "/api/ui/panels/p?sel=c&sel=gone&sel=a", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"selected"`) {
		t.Fatalf("got %d %s, want 200 from Select", rec.Code, rec.Body)
	}
	if got, want := f.selected(), []string{"c", "a"}; !slices.Equal(got, want) {
		t.Fatalf("Select got %v, want %v", got, want)
	}
	if f.defaults.Load() != 0 {
		t.Fatalf("View ran %d times on a selection that matched", f.defaults.Load())
	}
}

func TestSelectionOfOnlyUnknownIDsAnswersWithView(t *testing.T) {
	f := &pickerFake{choices: threeChoices()}
	h := buildWithPanels(t, f.panel("p"))
	rec := get(h, "/api/ui/panels/p?sel=gone&sel=left", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"default"`) {
		t.Fatalf("got %d %s, want 200 from View", rec.Code, rec.Body)
	}
	if f.selects.Load() != 0 || f.defaults.Load() != 1 {
		t.Fatalf("Select calls %d, View calls %d; want 0 and 1", f.selects.Load(), f.defaults.Load())
	}
}

func TestNoSelectionAnswersExactlyAsViewDoes(t *testing.T) {
	f := &pickerFake{choices: threeChoices()}
	h := buildWithPanels(t, f.panel("p"), testPanel("plain", 2, okView))
	rec := get(h, "/api/ui/panels/p", true)
	direct, err := json.Marshal(headed("default"))
	if err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != string(direct) {
		t.Fatalf("got %d %s, want 200 %s", rec.Code, rec.Body, direct)
	}
	if f.choicesCalls.Load() != 0 {
		t.Fatalf("Choices ran %d times with no selection", f.choicesCalls.Load())
	}
	if rec := get(h, "/api/ui/panels/plain", true); rec.Code != http.StatusOK {
		t.Fatalf("plain panel = %d, want 200", rec.Code)
	}
}

// encodedValue builds one sel value of chars characters, e of them
// percent-encoded, so its query bytes are exactly chars+2e.
func encodedValue(seed string, chars, e int) string {
	raw := seed + strings.Repeat("a", chars-len(seed))
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		if i < e {
			fmt.Fprintf(&b, "%%%02X", raw[i])
		} else {
			b.WriteByte(raw[i])
		}
	}
	return b.String()
}

// queryOfLength is ten fully encoded 64-character values plus one value
// sized so the whole raw query is exactly n bytes; every value is a valid
// distinct ID, so only the byte cap can refuse it.
func queryOfLength(t *testing.T, n int) string {
	t.Helper()
	var parts []string
	for i := range 10 {
		parts = append(parts, "sel="+encodedValue(fmt.Sprintf("v%d", i), 64, 64))
	}
	rest := n - len(strings.Join(parts, "&")) - 1 - len("sel=")
	chars := 64
	e := (rest - chars) / 2
	if (rest-chars)%2 != 0 {
		chars--
		e = (rest - chars) / 2
	}
	parts = append(parts, "sel="+encodedValue("last", chars, e))
	q := strings.Join(parts, "&")
	if len(q) != n {
		t.Fatalf("built a %d byte query, want %d", len(q), n)
	}
	return q
}

func TestSelectionQueryByteCap(t *testing.T) {
	f := &pickerFake{choices: threeChoices()}
	h := buildWithPanels(t, f.panel("p"))
	if rec := get(h, "/api/ui/panels/p?"+queryOfLength(t, 2048), true); rec.Code != http.StatusOK {
		t.Fatalf("2048 byte query = %d %s, want 200", rec.Code, rec.Body)
	}
	rec := get(h, "/api/ui/panels/p?"+queryOfLength(t, 2049), true)
	if rec.Code != http.StatusBadRequest || rec.Body.String() != refusalBody {
		t.Fatalf("2049 byte query = %d %s, want 400 %s", rec.Code, rec.Body, refusalBody)
	}
}

func TestSelectionRefusals(t *testing.T) {
	seventeen := make([]string, 17)
	sixteen := make([]string, 16)
	for i := range seventeen {
		seventeen[i] = fmt.Sprintf("sel=d%02d", i)
		if i < 16 {
			sixteen[i] = seventeen[i]
		}
	}
	cases := []struct {
		name  string
		query string
		leak  string // text a response or log must never carry
	}{
		{"17 values", strings.Join(seventeen, "&"), "d16"},
		{"duplicate", "sel=a&sel=b&sel=a", ""},
		{"markup", "sel=" + url.QueryEscape("<b>"), "<b>"},
		{"bad pattern space", "sel=has%20space", "has space"},
		{"65 characters", "sel=" + strings.Repeat("z", 65), strings.Repeat("z", 65)},
		{"empty value", "sel=", ""},
		{"no equals", "sel", ""},
		{"unknown key", "sel=a&other=secret-value", "secret-value"},
		{"only an unknown key", "other=secret-value", "secret-value"},
		{"bad escape", "sel=%zz", "%zz"},
		{"encoded slash", "sel=a%2Fb", "a/b"},
		{"semicolon separator", "sel=a;sel=b", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &pickerFake{choices: threeChoices()}
			h := buildWithPanels(t, f.panel("p"))
			var logs bytes.Buffer
			log.SetOutput(&logs)
			t.Cleanup(func() { log.SetOutput(testLogSink) })

			rec := get(h, "/api/ui/panels/p?"+tc.query, true)
			if rec.Code != http.StatusBadRequest || rec.Body.String() != refusalBody {
				t.Fatalf("got %d %s, want 400 %s", rec.Code, rec.Body, refusalBody)
			}
			if tc.leak != "" && (strings.Contains(rec.Body.String(), tc.leak) || strings.Contains(logs.String(), tc.leak)) {
				t.Fatalf("a requested value reached the body %q or the log %q", rec.Body, logs.String())
			}
			if n := f.choicesCalls.Load() + f.selects.Load() + f.defaults.Load(); n != 0 {
				t.Fatalf("%d panel calls ran for a refused request", n)
			}
			if rec := get(h, "/api/ui/panels/p", true); rec.Code != http.StatusOK {
				t.Fatalf("next plain read = %d, want 200: the refusal kept the busy flag", rec.Code)
			}
		})
	}
	t.Run("16 values pass", func(t *testing.T) {
		f := &pickerFake{choices: threeChoices()}
		h := buildWithPanels(t, f.panel("p"))
		if rec := get(h, "/api/ui/panels/p?"+strings.Join(sixteen, "&"), true); rec.Code != http.StatusOK {
			t.Fatalf("16 values = %d %s, want 200", rec.Code, rec.Body)
		}
	})
}

var testLogSink = log.Writer()

func TestAnyQueryOnAPanelWithNoPickerIsRefused(t *testing.T) {
	var calls atomic.Int32
	h := buildWithPanels(t, testPanel("plain", 1, func(context.Context) (sep2admin.Descriptor, error) {
		calls.Add(1)
		return sep2admin.Descriptor{Version: sep2admin.CurrentDescriptorVersion}, nil
	}))
	for _, q := range []string{"sel=x", "other=1"} {
		rec := get(h, "/api/ui/panels/plain?"+q, true)
		if rec.Code != http.StatusBadRequest || rec.Body.String() != refusalBody {
			t.Errorf("?%s = %d %s, want 400 %s", q, rec.Code, rec.Body, refusalBody)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("View ran %d times for refused requests", calls.Load())
	}
	if rec := get(h, "/api/ui/panels/plain", true); rec.Code != http.StatusOK || calls.Load() != 1 {
		t.Fatalf("no query = %d with %d View calls, want 200 and 1", rec.Code, calls.Load())
	}
}

func TestChoicesRouteBody(t *testing.T) {
	f := &pickerFake{choices: []sep2admin.Choice{{ID: "a.1", Label: "Alpha"}, {ID: "b", Label: "Beta"}}}
	h := buildWithPanels(t, f.panel("p"), testPanel("plain", 2, okView))
	rec := get(h, "/api/ui/panels/p/choices", true)
	want := `{"max":16,"choices":[{"id":"a.1","label":"Alpha"},{"id":"b","label":"Beta"}]}`
	if rec.Code != http.StatusOK || rec.Body.String() != want {
		t.Fatalf("got %d %s, want 200 %s", rec.Code, rec.Body, want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	f.choices = nil
	rec = get(h, "/api/ui/panels/p/choices", true)
	if want := `{"max":16,"choices":[]}`; rec.Body.String() != want {
		t.Fatalf("empty list body = %s, want the bytes %s", rec.Body, want)
	}

	for _, path := range []string{"/api/ui/panels/plain/choices", "/api/ui/panels/nope/choices"} {
		if rec := get(h, path, true); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	if rec := get(h, "/api/ui/panels/p/choices", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("choices from loopback with no credential = %d, want 401", rec.Code)
	}
}

func TestBadChoicesAnswer500OnBothRoutes(t *testing.T) {
	many := make([]sep2admin.Choice, 257)
	for i := range many {
		many[i] = sep2admin.Choice{ID: fmt.Sprintf("c%d", i), Label: fmt.Sprintf("L%d", i)}
	}
	cases := map[string][]sep2admin.Choice{
		"257 entries":     many,
		"duplicate id":    {{ID: "a", Label: "A"}, {ID: "a", Label: "B"}},
		"duplicate label": {{ID: "a", Label: "A"}, {ID: "b", Label: "A"}},
		"bad id":          {{ID: "a b", Label: "A"}},
		"empty label":     {{ID: "a", Label: ""}},
	}
	for name, ch := range cases {
		t.Run(name, func(t *testing.T) {
			f := &pickerFake{choices: ch}
			h := buildWithPanels(t, f.panel("p"))
			for _, path := range []string{"/api/ui/panels/p/choices", "/api/ui/panels/p?sel=a"} {
				rec := get(h, path, true)
				if rec.Code != http.StatusInternalServerError || rec.Body.String() != `{"error":"panel failed"}` {
					t.Errorf("GET %s = %d %s, want 500 panel failed", path, rec.Code, rec.Body)
				}
			}
			if f.selects.Load() != 0 {
				t.Errorf("Select ran %d times on a bad choice list", f.selects.Load())
			}
		})
	}
}

func TestChoicesErrorAnswers500WithoutItsText(t *testing.T) {
	f := &pickerFake{choicesErr: errors.New("db password hunter2")}
	h := buildWithPanels(t, f.panel("p"))
	for _, path := range []string{"/api/ui/panels/p/choices", "/api/ui/panels/p?sel=a"} {
		rec := get(h, path, true)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "hunter2") {
			t.Errorf("GET %s = %d %s, want 500 with no error text", path, rec.Code, rec.Body)
		}
	}
}

func TestHungChoicesAnswers504AndFreesThePanelAfterwards(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	f := &pickerFake{}
	p := f.panel("p")
	var calls atomic.Int32
	p.Picker.Choices = func(context.Context) ([]sep2admin.Choice, error) {
		calls.Add(1)
		<-release
		return threeChoices(), nil
	}
	h := routerFor(t, 20*time.Millisecond, p)

	if rec := get(h, "/api/ui/panels/p/choices", true); rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("GET choices with Choices hung = %d, want 504", rec.Code)
	}
	// The hung call still holds the flag: a second read is refused, not stacked.
	if rec := get(h, "/api/ui/panels/p?sel=a", true); rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("selection read while Choices still hung = %d, want 504", rec.Code)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("Choices invoked %d times across two reads of a hung panel, want 1", n)
	}
	once.Do(func() { close(release) })
	deadline := time.Now().Add(2 * time.Second)
	for {
		rec := get(h, "/api/ui/panels/p", true)
		if rec.Code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("View still blocked after Choices returned: %d %s", rec.Code, rec.Body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHungSelectAnswers504(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f := &pickerFake{choices: threeChoices()}
	p := f.panel("p")
	p.Picker.Select = func(context.Context, sep2admin.Selection) (sep2admin.Descriptor, error) {
		<-release
		return sep2admin.Descriptor{}, nil
	}
	h := routerFor(t, 20*time.Millisecond, p)
	if rec := get(h, "/api/ui/panels/p?sel=a", true); rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("hung Select = %d, want 504", rec.Code)
	}
}

func TestSelectDescriptorIsCheckedLikeView(t *testing.T) {
	f := &pickerFake{choices: threeChoices()}
	p := f.panel("p")
	p.Picker.Select = func(context.Context, sep2admin.Selection) (sep2admin.Descriptor, error) {
		return sep2admin.Descriptor{Version: 99}, nil
	}
	h := buildWithPanels(t, p)
	if rec := get(h, "/api/ui/panels/p?sel=a", true); rec.Code != http.StatusInternalServerError {
		t.Fatalf("Select with a wrong descriptor version = %d, want 500", rec.Code)
	}
}

func TestAnUnknownIDIsNotLogged(t *testing.T) {
	f := &pickerFake{choices: threeChoices()}
	h := buildWithPanels(t, f.panel("p"))
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(testLogSink) })
	rec := get(h, "/api/ui/panels/p?sel=departed-battery&sel=a", true)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "departed-battery") || strings.Contains(logs.String(), "departed-battery") {
		t.Fatalf("got %d %s with log %q: a requested id was echoed", rec.Code, rec.Body, logs.String())
	}
}
