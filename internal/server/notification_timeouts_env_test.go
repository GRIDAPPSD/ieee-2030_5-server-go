package server

import (
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
