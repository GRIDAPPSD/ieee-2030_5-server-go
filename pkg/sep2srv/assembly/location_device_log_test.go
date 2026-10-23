package assembly_test

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeviceLogRecordsLocationOfCreateOnly proves the line for a POST /mup
// carries the Location the 201 answered with, read back from the response the
// client saw, and that the GET of the created mirror carries none.
func TestDeviceLogRecordsLocationOfCreateOnly(t *testing.T) {
	t.Parallel()
	srv, dir, closeLog := logServer(t, gateTestPolicy())
	warmDeviceLog(t, srv, dir, victimLFDI, "/edev/"+victimID)

	body := `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>LOCLOG0001</mRID></MirrorUsagePoint>`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/mup", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(gateIdentityHeader, victimLFDI)
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := isolatedClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusCreated || !strings.HasPrefix(loc, "/mup/") {
		t.Fatalf("control: POST /mup = %d Location %q, want 201 and /mup/...", resp.StatusCode, loc)
	}
	if status, _ := gateRequest(t, srv, http.MethodGet, loc, victimLFDI, ""); status != http.StatusOK {
		t.Fatalf("control: GET %s = %d, want 200", loc, status)
	}
	closeLog()

	file := filepath.Join(dir, victimLFDI+".jsonl")
	raw, posts := linesFor(t, file, "/mup")
	var created []map[string]any
	for _, l := range posts {
		if l["method"] == http.MethodPost {
			created = append(created, l)
		}
	}
	if len(created) != 1 {
		t.Fatalf("got %d POST /mup lines, want 1: %s", len(created), raw)
	}
	if created[0]["location"] != loc {
		t.Errorf("POST /mup location = %v, want %q", created[0]["location"], loc)
	}
	raw, gets := linesFor(t, file, loc)
	if len(gets) != 1 {
		t.Fatalf("got %d lines for GET %s, want 1: %s", len(gets), loc, raw)
	}
	if v, present := gets[0]["location"]; present {
		t.Errorf("GET line carries location %v, want none", v)
	}
}
