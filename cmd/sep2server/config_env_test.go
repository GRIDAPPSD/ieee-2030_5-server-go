package main

import (
	"os"
	"testing"
)

// TestConfigFromEnvNoHomeWithAllCertPathsSet pins review finding 2 (#601):
// a deployment that sets every certificate path explicitly must not fail
// startup just because $HOME is undeterminable, since resolveCertDir's
// result would never be used. configFromEnv is what runServe calls before
// the server binds, so a passing case here is the closest unit-level proxy
// for "serve starts" without actually binding a listener.
func TestConfigFromEnvNoHomeWithAllCertPathsSet(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("SEP2_CERT_DIR", "")
	t.Setenv("SEP2_CERT", "/etc/tls/server.crt")
	t.Setenv("SEP2_KEY", "/etc/tls/server.key")
	t.Setenv("SEP2_CA", "/etc/tls/ca.crt")

	r := &certDirResolver{}
	cfg, err := configFromEnv(r)
	if err != nil {
		t.Fatalf("configFromEnv with every cert path set and no HOME: unexpected error: %v", err)
	}
	if cfg.CertFile != "/etc/tls/server.crt" || cfg.KeyFile != "/etc/tls/server.key" || cfg.CAFile != "/etc/tls/ca.crt" {
		t.Fatalf("cfg = %+v, want the explicit paths unchanged", cfg)
	}
	if r.resolved {
		t.Fatal("the default certificate directory was resolved even though every path was explicit")
	}
}

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

			cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
			if err != nil {
				t.Fatalf("configFromEnv: %v", err)
			}
			if got := cfg.NotificationAllowLoopback; got != tc.want {
				t.Errorf("NotificationAllowLoopback = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestConfigFromEnvTrafficCapture pins the #628 permit (main.go:175) to the
// same strict-equality idiom TestConfigFromEnvNotificationAllowLoopback
// already pins: only the exact literal "true" turns capture on, so a
// deployment that never sets the variable, or sets a near-miss, stays off.
func TestConfigFromEnvTrafficCapture(t *testing.T) {
	const key = "SEP2_TRAFFIC_CAPTURE"
	value := func(s string) *string { return &s }

	for _, tc := range []struct {
		name  string
		value *string // nil means unset
		want  bool
	}{
		{"unset stays off", nil, false},
		{"empty stays off", value(""), false},
		{"true opts in", value("true"), true},
		{"TRUE stays off", value("TRUE"), false},
		{"1 stays off", value("1"), false},
		{"yes stays off", value("yes"), false},
		{"false stays off", value("false"), false},
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

			cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
			if err != nil {
				t.Fatalf("configFromEnv: %v", err)
			}
			if got := cfg.TrafficCapture; got != tc.want {
				t.Errorf("TrafficCapture = %v, want %v", got, tc.want)
			}
		})
	}
}
