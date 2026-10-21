package handler_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

// The descriptor is additive: the response's name, value and description,
// decoded and encoded again, are the bytes the catalog served before it had
// one. The legacy golden is read, never rewritten.
func TestControlFunctions_LegacyKeysUnchanged(t *testing.T) {
	x := newControlValuesFixture(t)
	w := x.f.do(http.MethodGet, "/api/der/control-functions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var body struct {
		Functions []struct {
			Name        string `json:"name"`
			Value       string `json:"value"`
			Description string `json:"description"`
		} `json:"functions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/control_functions.golden.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("legacy keys differ\n--- got ---\n%s", buf.Bytes())
	}
}

func TestControlFunctions_KindGolden(t *testing.T) {
	x := newControlValuesFixture(t)
	w := x.f.do(http.MethodGet, "/api/der/control-functions", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	checkGolden(t, "testdata/control_functions_kinds.golden.json", w.Body.Bytes())
}
