package assembly_test

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
)

// deviceLogServer is lelServer with a device log writer in the router config.
func deviceLogServer(t *testing.T) (*httptest.Server, func() [][]byte) {
	t.Helper()
	dir := t.TempDir()
	w, err := devicelog.New(devicelog.Config{Dir: dir, MaxBytes: 1_000_000})
	if err != nil {
		t.Fatalf("devicelog.New: %v", err)
	}
	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "d1", "d2")
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{DeviceLog: w},
		stores, testAuthPolicy(), testSFDI, testLFDI, nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	lines := func() [][]byte {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(dir, testLFDI+".jsonl"))
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			t.Fatalf("read log: %v", err)
		}
		var out [][]byte
		for _, l := range bytes.Split(raw, []byte{'\n'}) {
			// The request observer writes a request line for the same POST
			// into the same file; these tests are about the LogEvent line.
			if bytes.Contains(l, []byte(`"kind":"logevent"`)) {
				out = append(out, l)
			}
		}
		return out
	}
	return srv, lines
}

func TestLogEvent_PostWritesOneDeviceLogLineKeyedByTheCertIdentity(t *testing.T) {
	srv, lines := deviceLogServer(t)
	evt := sampleLogEvent(17, 5)
	evt.Details = "line one\nline two"
	loc := postLogEvent(t, srv, "d1", evt)

	got := lines()
	if len(got) != 1 {
		t.Fatalf("device log lines = %d, want 1", len(got))
	}
	var rec map[string]any
	if err := json.Unmarshal(got[0], &rec); err != nil {
		t.Fatalf("line is not one JSON object: %v: %s", err, got[0])
	}
	want := map[string]any{
		"kind": "logevent", "actor": testLFDI, "edev": "d1", "href": loc,
		"functionSet": float64(evt.FunctionSet), "logEventCode": 5.0,
		"logEventID": 17.0, "logEventPEN": 54465.0, "profileID": 2.0,
		"createdDateTime": float64(evt.CreatedDateTime),
		"extendedData":    float64(*evt.ExtendedData),
		"details":         `line one\nline two`,
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("field %s = %v, want %v", k, rec[k], v)
		}
	}
}

func TestLogEvent_RefusedPostWritesNoDeviceLogLine(t *testing.T) {
	srv, lines := deviceLogServer(t)
	resp, err := srv.Client().Post(srv.URL+"/edev/d1/lel", "application/sep+xml", strings.NewReader("<LogEvent><unclosed>"))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad XML status = %d, want 400", resp.StatusCode)
	}
	if got := lines(); len(got) != 0 {
		t.Fatalf("device log lines = %d, want 0 after a refused POST", len(got))
	}
}

func TestLogEvent_DeviceLogLineCarriesNoQueryOrHeaderText(t *testing.T) {
	srv, lines := deviceLogServer(t)
	body, err := xml.Marshal(func() any { e := sampleLogEvent(3, 1); return &e }())
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/edev/d1/lel?secret=QUERYTOKEN", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer HEADERTOKEN")
	req.Header.Set("Content-Type", "application/sep+xml")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	got := lines()
	if len(got) != 1 {
		t.Fatalf("device log lines = %d, want 1", len(got))
	}
	for _, leak := range []string{"QUERYTOKEN", "HEADERTOKEN", "secret"} {
		if bytes.Contains(got[0], []byte(leak)) {
			t.Errorf("line carries %q: %s", leak, got[0])
		}
	}
}

func TestLogEvent_NoDeviceLogConfiguredStillServes(t *testing.T) {
	srv, _ := lelServer(t)
	postLogEvent(t, srv, "d1", sampleLogEvent(1, 1))
}
