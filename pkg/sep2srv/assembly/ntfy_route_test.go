package assembly_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/devicelog"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/reqlimit"
)

const ntfyBody = `<Notification xmlns="urn:ieee:std:2030.5:ns">` +
	`<subscribedResource>/edev/d1/der</subscribedResource>` +
	`<subscriptionURI>/edev/d1/sub/s1</subscriptionURI>` +
	`<status>2</status>` +
	`<newResourceURI>/edev/d1/der/9</newResourceURI>` +
	`</Notification>`

const otherLFDI = "0011223344556677889900112233445566778899"

// ntfyServer serves the router over a device log in dir. identity nil keeps
// the default test identity (testLFDI, a registered device).
func ntfyServer(t *testing.T, logOn bool, identity func(context.Context) (string, string, bool)) (srv *httptest.Server, dir string, notificationLines func() []map[string]any) {
	t.Helper()
	dir = t.TempDir()
	cfg := assembly.RouterConfig{}
	var w *devicelog.Writer
	if logOn {
		var err error
		w, err = devicelog.New(devicelog.Config{Dir: dir, MaxBytes: 1_000_000})
		if err != nil {
			t.Fatalf("devicelog.New: %v", err)
		}
		cfg.DeviceLog = w
	}
	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "d1")
	pol := testAuthPolicy()
	if identity != nil {
		pol.Identity = identity
	}
	handler, _ := assembly.BuildProtocolRouter(cfg, stores, pol, testSFDI, testLFDI, nil)
	srv = httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	if logOn && identity == nil {
		// Until the device's registration lookup settles, its lines go to the
		// shared file; warm it so the line lands in the device's own.
		warmDeviceLog(t, srv, dir, testLFDI, "/edev/d1")
	}

	notificationLines = func() []map[string]any {
		if w == nil {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := w.Close(ctx); err != nil {
			t.Fatalf("Close: %v", err)
		}
		var out []map[string]any
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read dir: %v", err)
		}
		for _, e := range ents {
			raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			for _, l := range bytes.Split(raw, []byte{'\n'}) {
				if !bytes.Contains(l, []byte(`"kind":"notification"`)) {
					continue
				}
				var m map[string]any
				if err := json.Unmarshal(l, &m); err != nil {
					t.Fatalf("line %q is not JSON: %v", l, err)
				}
				m["file"] = e.Name()
				out = append(out, m)
			}
		}
		return out
	}
	return srv, dir, notificationLines
}

func ntfyDo(t *testing.T, srv *httptest.Server, method, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+"/ntfy", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestNtfy_ValidNotificationIsAcceptedAndRecordedInTheDevicesLog(t *testing.T) {
	srv, _, lines := ntfyServer(t, true, nil)
	resp, body := ntfyDo(t, srv, http.MethodPost, ntfyBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", resp.StatusCode, body)
	}
	if loc := resp.Header.Get("Location"); loc != "/ntfy" {
		t.Errorf("Location = %q, want /ntfy", loc)
	}
	got := lines()
	if len(got) != 1 {
		t.Fatalf("notification lines = %d, want 1: %v", len(got), got)
	}
	want := map[string]any{
		"kind":               "notification",
		"actor":              testLFDI,
		"file":               testLFDI + ".jsonl",
		"status":             2.0,
		"subscribedResource": "/edev/d1/der",
		"subscriptionURI":    "/edev/d1/sub/s1",
		"newResourceURI":     "/edev/d1/der/9",
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("line[%q] = %v, want %v", k, got[0][k], v)
		}
	}
}

func TestNtfy_InvalidBodiesAnswer400AndRecordNothing(t *testing.T) {
	const ns = `xmlns="urn:ieee:std:2030.5:ns"`
	cases := map[string]string{
		"not XML":              "nope",
		"empty":                "",
		"wrong root":           `<Subscription ` + ns + `/>`,
		"reserved status":      `<Notification ` + ns + `><subscribedResource>/r</subscribedResource><subscriptionURI>/s</subscriptionURI><status>5</status></Notification>`,
		"truncated at the cap": ntfyBody[:len(ntfyBody)-len("</Notification>")] + strings.Repeat(" ", reqlimit.MaxBody) + `</Notification>`,
	}
	srv, _, lines := ntfyServer(t, true, nil)
	for name, body := range cases {
		resp, _ := ntfyDo(t, srv, http.MethodPost, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
	if got := lines(); len(got) != 0 {
		t.Errorf("recorded %d lines for invalid bodies, want 0: %v", len(got), got)
	}
}

func TestNtfy_ElementOnlyNotificationIsAcceptedAndLoggedAsSent(t *testing.T) {
	srv, _, lines := ntfyServer(t, true, nil)
	resp, body := ntfyDo(t, srv, http.MethodPost, `<Notification xmlns="urn:ieee:std:2030.5:ns"/>`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200", resp.StatusCode, body)
	}
	got := lines()
	if len(got) != 1 || got[0]["status"] != 0.0 || got[0]["subscribedResource"] != "" {
		t.Errorf("lines = %v, want one with status 0 and empty subscribedResource", got)
	}
}

func TestNtfy_GetAndHeadAnswer405(t *testing.T) {
	for _, logOn := range []bool{true, false} {
		srv, _, _ := ntfyServer(t, logOn, nil)
		for _, m := range []string{http.MethodGet, http.MethodHead} {
			resp, _ := ntfyDo(t, srv, m, "")
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("log on=%v %s: status = %d, want 405", logOn, m, resp.StatusCode)
			}
			if allow := resp.Header.Get("Allow"); !strings.Contains(allow, "POST") {
				t.Errorf("log on=%v %s: Allow = %q, want POST listed", logOn, m, allow)
			}
		}
	}
}

func TestNtfy_UnknownOrAnonymousDeviceIsRefusedWith403AndRecordsNothing(t *testing.T) {
	ids := map[string]func(context.Context) (string, string, bool){
		"unregistered device": func(context.Context) (string, string, bool) { return otherLFDI, "", true },
		"no identity":         func(context.Context) (string, string, bool) { return "", "", false },
		"non-canonical LFDI":  func(context.Context) (string, string, bool) { return "not-an-lfdi", "", true },
	}
	for name, id := range ids {
		srv, _, lines := ntfyServer(t, true, id)
		resp, _ := ntfyDo(t, srv, http.MethodPost, ntfyBody)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status = %d, want 403", name, resp.StatusCode)
		}
		if got := lines(); len(got) != 0 {
			t.Errorf("%s: recorded %v, want nothing", name, got)
		}
	}
}

func TestNtfy_DeviceLogOffRefusesWith503(t *testing.T) {
	srv, _, _ := ntfyServer(t, false, nil)
	resp, _ := ntfyDo(t, srv, http.MethodPost, ntfyBody)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}
