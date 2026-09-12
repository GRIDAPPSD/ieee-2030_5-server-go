package main

import (
	"os"
	"testing"
)

func TestConfigFromEnvNotificationAllowLoopback(t *testing.T) {
	const key = "SEP2_NOTIFICATION_ALLOW_LOOPBACK"
	value := func(s string) *string { return &s }

	for _, tc := range []struct {
		name  string
		value *string // nil means unset
		want  bool
	}{
		{"unset stays strict", nil, false},
		{"empty stays strict", value(""), false},
		{"true opts in", value("true"), true},
		{"TRUE stays strict", value("TRUE"), false},
		{"1 stays strict", value("1"), false},
		{"yes stays strict", value("yes"), false},
		{"false stays strict", value("false"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(key, "")
			if tc.value == nil {
				if err := os.Unsetenv(key); err != nil {
					t.Fatalf("unset %s: %v", key, err)
				}
			} else {
				t.Setenv(key, *tc.value)
			}

			if got := configFromEnv().NotificationAllowLoopback; got != tc.want {
				t.Errorf("NotificationAllowLoopback = %v, want %v", got, tc.want)
			}
		})
	}
}
