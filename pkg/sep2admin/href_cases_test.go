package sep2admin

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// testdata/href_cases.json is shared with the admin UI's safeHref test, so
// the encoder and the renderer are held to the same list.
func TestCheckHrefSharedCases(t *testing.T) {
	raw, err := os.ReadFile("testdata/href_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Href string `json:"href"`
		OK   bool   `json:"ok"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) == 0 {
		t.Fatal("no cases read")
	}
	for _, c := range cases {
		err := checkHref(c.Href)
		if c.OK && err != nil {
			t.Errorf("checkHref(%q) = %v, want accepted", c.Href, err)
		}
		if !c.OK && !errors.Is(err, ErrUnsafeLink) {
			t.Errorf("checkHref(%q) = %v, want ErrUnsafeLink", c.Href, err)
		}
	}
}
