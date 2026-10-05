package sep2adminplane_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2adminplane"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/activity"
)

// planeComms builds a plane whose recorder saw device AA at seen, and reads
// AA's comms state and the threshold from GET /dashboard/data.
func planeComms(t *testing.T, seen time.Time) (string, float64) {
	t.Helper()
	rec := activity.NewWithClock(func() time.Time { return seen })
	rec.Record("AA")

	cfg := baseConfig()
	cfg.LoopbackBypass = true
	cfg.Activity = rec
	cfg.CommsOfflineAfter = time.Minute
	if err := cfg.Stores.EndDevices.Create(context.Background(), "1", sep2.EndDevice{SFDI: "111", LFDI: "AA"}); err != nil {
		t.Fatal(err)
	}
	w := send(newPlane(t, cfg), http.MethodGet, "/dashboard/data", "", false)
	var body struct {
		Devices []struct {
			Comms string `json:"comms"`
		} `json:"devices"`
		After float64 `json:"commsOfflineAfterSeconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Devices) != 1 {
		t.Fatalf("GET /dashboard/data = %d %s (err %v), want one device", w.Code, w.Body, err)
	}
	return body.Devices[0].Comms, body.After
}

func TestPlaneDevicesReportCommsFromActivity(t *testing.T) {
	// The dashboard reads the wall clock, so the recorded request is placed
	// relative to it against a threshold of one minute.
	if comms, after := planeComms(t, time.Now()); comms != "online" || after != 60 {
		t.Errorf("a request just made: comms %q, threshold %v, want online and 60", comms, after)
	}
	if comms, _ := planeComms(t, time.Now().Add(-time.Minute-time.Second)); comms != "offline" {
		t.Errorf("a request past the threshold: comms %q, want offline", comms)
	}
}

func TestPlaneWithoutActivityReportsUnknown(t *testing.T) {
	cfg := baseConfig()
	cfg.LoopbackBypass = true
	if err := cfg.Stores.EndDevices.Create(context.Background(), "1", sep2.EndDevice{SFDI: "111", LFDI: "AA"}); err != nil {
		t.Fatal(err)
	}
	w := send(newPlane(t, cfg), http.MethodGet, "/dashboard/data", "", false)
	var body struct {
		Devices []struct {
			Comms string `json:"comms"`
		} `json:"devices"`
		After float64 `json:"commsOfflineAfterSeconds"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Devices) != 1 {
		t.Fatalf("GET /dashboard/data = %d %s (err %v)", w.Code, w.Body, err)
	}
	if body.Devices[0].Comms != "unknown" || body.After != 300 {
		t.Errorf("no recorder: comms %q, threshold %v, want unknown and the 300 default", body.Devices[0].Comms, body.After)
	}
}

func TestNewRefusesCommsOfflineAfterUnderOneSecond(t *testing.T) {
	for _, d := range []time.Duration{-time.Second, time.Nanosecond, 999 * time.Millisecond} {
		cfg := baseConfig()
		cfg.CommsOfflineAfter = d
		if _, err := sep2adminplane.New(cfg); !errors.Is(err, sep2adminplane.ErrBadCommsOfflineAfter) {
			t.Errorf("New(CommsOfflineAfter %v) = %v, want ErrBadCommsOfflineAfter", d, err)
		}
	}
	for _, d := range []time.Duration{0, time.Second} {
		cfg := baseConfig()
		cfg.CommsOfflineAfter = d
		if _, err := sep2adminplane.New(cfg); err != nil {
			t.Errorf("New(CommsOfflineAfter %v) = %v, want accepted", d, err)
		}
	}
}
