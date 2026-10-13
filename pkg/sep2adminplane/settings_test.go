package sep2adminplane_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2adminplane"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestSettingsFromEnvNamesTheBadVariable(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"edition", map[string]string{"SEP2_EDITION": "2030"}, "SEP2_EDITION"},
		{"pen past uint32", map[string]string{"SEP2_PEN": "4294967296"}, "SEP2_PEN"},
		{"deadline above 3600", map[string]string{"SEP2_FLOW_RESERVATION_DEADLINE_SECONDS": "3601"}, "SEP2_FLOW_RESERVATION_DEADLINE_SECONDS"},
		{"grace below 900", map[string]string{"SEP2_FLOW_RESERVATION_RETENTION_GRACE_SECONDS": "899"}, "SEP2_FLOW_RESERVATION_RETENTION_GRACE_SECONDS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sep2adminplane.SettingsFromEnv(envOf(tc.env))
			if err == nil {
				t.Fatal("SettingsFromEnv accepted the value")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %s", err, tc.want)
			}
		})
	}
}

func TestSettingsFromEnvParsesEveryValue(t *testing.T) {
	s, err := sep2adminplane.SettingsFromEnv(envOf(map[string]string{
		"SEP2_EDITION":                           "2023",
		"SEP2_PEN":                               "4294967295",
		"SEP2_FLOW_RESERVATION_DEADLINE_SECONDS": "90",
		"SEP2_FLOW_RESERVATION_RETENTION_GRACE_SECONDS": "900",
	}))
	if err != nil {
		t.Fatalf("SettingsFromEnv: %v", err)
	}
	if s.Edition != "2023" || s.PEN == nil || *s.PEN != 4294967295 ||
		s.FlowReservationDeadline != 90*time.Second || s.RetentionGrace != 900*time.Second {
		t.Fatalf("Settings = %+v (PEN %v)", s, s.PEN)
	}
}

func TestSettingsFromEnvUnsetIsZero(t *testing.T) {
	s, err := sep2adminplane.SettingsFromEnv(envOf(nil))
	if err != nil {
		t.Fatalf("SettingsFromEnv: %v", err)
	}
	if s.Edition != "" || s.PEN != nil || s.FlowReservationDeadline != 0 || s.RetentionGrace != 0 {
		t.Fatalf("unset Settings = %+v, want all zero", s)
	}
}

// PEN 0 is reserved, so the plane mints nothing and create stays 503.
func TestPENZeroLeavesDERControlCreate503(t *testing.T) {
	s, err := sep2adminplane.SettingsFromEnv(envOf(map[string]string{"SEP2_PEN": "0"}))
	if err != nil {
		t.Fatalf("SettingsFromEnv: %v", err)
	}
	cfg := baseConfig()
	cfg.ControlWrites = true
	cfg.PEN = s.PEN
	p := newPlane(t, cfg)
	if rec := send(p, http.MethodPost, "/api/der/controls", `{"derProgramHref":"/edev/0/fsa/0/derp/0","type":"connect","durationSeconds":300}`, true); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /api/der/controls with PEN 0 = %d %s, want 503", rec.Code, rec.Body)
	}
}
