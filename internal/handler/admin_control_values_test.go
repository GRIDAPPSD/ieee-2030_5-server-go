package handler_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"testing"
)

// #992: every route that takes control values parses them the same way. The
// goldens pin each route's 201 body and refusals so the parse can move
// without a visible change.

var updateControlValues = flag.Bool("update-control-values", false, "rewrite the control values goldens")

// cvValues are the control value members every route accepts, each of which
// must parse the same on every route.
var cvValues = []struct{ name, members string }{
	{"maxLimW", `"type":"maxLimW","maxLimW":4000`},
	{"connect", `"type":"connect"`},
	{"fixedPF", `"type":"fixedPFInjectW","powerFactor":{"displacement":900,"excitation":true}`},
	{"targetW", `"type":"targetW","targetW":{"value":500,"multiplier":0}`},
	{"functions", `"functions":{"opModMaxLimW":5000,"opModTargetW":{"value":500,"multiplier":0}}`},
}

// cvRoute is one route and the body it takes around a set of control value
// members.
type cvRoute struct {
	name, method string
	path         func(x *fsaControlFixture) string
	body         func(members string) string
}

func cvRoutes() []cvRoute {
	sched := func(members string) string {
		return fmt.Sprintf(`{%s,"startTime":%d,"durationSeconds":600}`, members, futureStart(900))
	}
	program := func(members string) string {
		return `{"description":"cv","primacy":1,"defaultControl":{` + members + `}}`
	}
	return []cvRoute{
		{"der-controls", http.MethodPost, func(*fsaControlFixture) string { return "/api/der/controls" },
			func(m string) string { return sched(`"derProgramHref":"/edev/0/fsa/0/derp/0",` + m) }},
		{"device-scheduled", http.MethodPost, func(*fsaControlFixture) string { return "/api/devices/1/controls" },
			func(m string) string { return sched(`"kind":"scheduled",` + m) }},
		{"device-default", http.MethodPost, func(*fsaControlFixture) string { return "/api/devices/1/controls" },
			func(m string) string { return `{"kind":"default",` + m + `}` }},
		{"fsa-control", http.MethodPost, func(x *fsaControlFixture) string { return x.path() }, sched},
		{"fsa-program-create", http.MethodPost, func(*fsaControlFixture) string { return "/api/fsas/fsa-A/der-programs" }, program},
		{"fsa-program-edit", http.MethodPut, func(x *fsaControlFixture) string { return templatePath("fsa-A", x.tid) }, program},
		{"device-program-create", http.MethodPost, func(*fsaControlFixture) string { return "/api/devices/1/fsas/fsa-A/der-programs" }, program},
		{"device-program-edit", http.MethodPut, func(x *fsaControlFixture) string { return devicePath("1", "0") }, program},
	}
}

func newControlValuesFixture(t *testing.T) *fsaControlFixture {
	t.Helper()
	x := newFSAControlFixture(t)
	wireDeviceControl(x.dc, x.f)
	x.f.mux.HandleFunc("POST /api/der/controls", x.dc.h.HandleCreate())
	x.f.mux.HandleFunc("GET /api/der/control-functions", x.dc.h.HandleFunctions())
	x.f.assign(t, "1", "fsa-A")
	seedFixtureProgram(t, x.f, "1", "0", "0", true)
	return x
}

var cvVolatile = []struct {
	re   *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`derp-[0-9a-f]{16}`), "MINTED-PROGRAM"},
	{regexp.MustCompile(`[0-9A-F]{32,}`), "MINTED-ID"},
}

var cvVolatileKeys = map[string]bool{"creationTime": true, "start": true, "dateTime": true, "startTime": true}

// normalizeVolatile blanks the minted ids and clock-derived numbers in a
// decoded body, so the golden holds only what the route decided.
func normalizeVolatile(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if cvVolatileKeys[k] {
				if _, isNum := e.(json.Number); isNum {
					x[k] = json.Number("0")
					continue
				}
			}
			x[k] = normalizeVolatile(e)
		}
		return x
	case []any:
		for i := range x {
			x[i] = normalizeVolatile(x[i])
		}
		return x
	case string:
		for _, v := range cvVolatile {
			x = v.re.ReplaceAllString(x, v.name)
		}
		return x
	}
	return v
}

type cvOutcome struct {
	Status int `json:"status"`
	Body   any `json:"body"`
}

func cvOutcomeOf(t *testing.T, code int, raw []byte) cvOutcome {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var body any
	if err := dec.Decode(&body); err != nil {
		t.Fatalf("body %q: %v", raw, err)
	}
	return cvOutcome{Status: code, Body: normalizeVolatile(body)}
}

// checkGolden compares got with the file, or rewrites it under the flag.
func checkGolden(t *testing.T, file string, got []byte) {
	t.Helper()
	if *updateControlValues {
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs\n--- got ---\n%s", file, got)
	}
}

func TestControlValues_RouteGolden(t *testing.T) {
	got := map[string]cvOutcome{}
	for _, rt := range cvRoutes() {
		for _, v := range cvValues {
			x := newControlValuesFixture(t)
			w := x.f.do(rt.method, rt.path(x), rt.body(v.members))
			if w.Code != http.StatusCreated && w.Code != http.StatusOK {
				t.Errorf("%s/%s: status %d body %s, want a success", rt.name, v.name, w.Code, w.Body.String())
			}
			got[rt.name+"/"+v.name] = cvOutcomeOf(t, w.Code, w.Body.Bytes())
		}
		refusals := []struct{ name, members string }{
			{"unknownField", `"type":"maxLimW","maxLimW":4000,"bogus":1`},
			{"maxLimWNotInteger", `"type":"maxLimW","maxLimW":"x"`},
			{"displacementNotInteger", `"type":"fixedPFInjectW","powerFactor":{"displacement":"x","excitation":true}`},
			{"targetWValueNotInteger", `"type":"targetW","targetW":{"value":"x"}`},
			{"typeAndFunctions", `"type":"maxLimW","maxLimW":4000,"functions":{"opModConnect":true}`},
			{"typeRequired", `"maxLimW":4000`},
			{"typeUnknown", `"type":"nope"`},
			{"maxLimWRange", `"type":"maxLimW","maxLimW":10001`},
		}
		for _, r := range refusals {
			x := newControlValuesFixture(t)
			w := x.f.do(rt.method, rt.path(x), rt.body(r.members))
			if w.Code < 400 || w.Code > 499 {
				t.Errorf("%s/%s: status %d body %s, want a 4xx refusal", rt.name, r.name, w.Code, w.Body.String())
			}
			got[rt.name+"/"+r.name] = cvOutcomeOf(t, w.Code, w.Body.Bytes())
		}
	}
	out, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, "testdata/control_values_routes.golden.json", append(out, '\n'))
}

func TestControlValues_FunctionsGolden(t *testing.T) {
	x := newControlValuesFixture(t)
	w := x.f.do(http.MethodGet, "/api/der/control-functions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	checkGolden(t, "testdata/control_functions.golden.json", w.Body.Bytes())
}
