package server

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNotificationTimeoutsFromEnv(t *testing.T) {
	t.Setenv("SEP2_NOTIFICATION_POST_TIMEOUT", "45s")
	t.Setenv("SEP2_NOTIFICATION_DIAL_TIMEOUT", " 2m ")
	t.Setenv("SEP2_NOTIFICATION_RESOLVE_TIMEOUT", "1500ms")
	got := notificationTimeoutsFromEnv()
	if got.Post != 45*time.Second || got.Dial != 2*time.Minute || got.CreationResolve != 1500*time.Millisecond {
		t.Errorf("got %+v, want Post=45s Dial=2m CreationResolve=1.5s", got)
	}
}

func TestNotificationTimeoutsFromEnvInvalidKeepsDefault(t *testing.T) {
	for _, bad := range []string{"", "soon", "30", "0s", "-5s"} {
		t.Setenv("SEP2_NOTIFICATION_POST_TIMEOUT", bad)
		t.Setenv("SEP2_NOTIFICATION_DIAL_TIMEOUT", bad)
		t.Setenv("SEP2_NOTIFICATION_RESOLVE_TIMEOUT", bad)
		if got := notificationTimeoutsFromEnv(); got.Post != 0 || got.Dial != 0 || got.CreationResolve != 0 {
			t.Errorf("%q: got %+v, want all zero (defaults kept)", bad, got)
		}
	}
}

func TestNotificationTimeoutsFromEnvLogsWarningNamingVariable(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	t.Setenv("SEP2_NOTIFICATION_POST_TIMEOUT", "")
	t.Setenv("SEP2_NOTIFICATION_DIAL_TIMEOUT", "soon")
	t.Setenv("SEP2_NOTIFICATION_RESOLVE_TIMEOUT", "-5s")
	notificationTimeoutsFromEnv()
	out := buf.String()
	for _, name := range []string{"SEP2_NOTIFICATION_DIAL_TIMEOUT", "SEP2_NOTIFICATION_RESOLVE_TIMEOUT"} {
		if !strings.Contains(out, "WARNING: "+name+"=") {
			t.Errorf("log %q has no warning naming %s", out, name)
		}
	}
	if strings.Contains(out, "SEP2_NOTIFICATION_POST_TIMEOUT") {
		t.Errorf("an empty variable logged a warning: %q", out)
	}
}
