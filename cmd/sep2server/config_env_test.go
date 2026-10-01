package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
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

// TestConfigFromEnvServingAndDeviceCAUnsetLeaveConfigEmpty pins #622 item 1:
// with neither new setting exported, configFromEnv must leave
// ServingCAFile/DeviceCAFile empty rather than defaulting them to the
// resolved CAFile path (or to the cert-dir fallback envPathOrCertDir would
// apply). Config.EffectiveServingCA/EffectiveDeviceCA do the CAFile
// fallback; if configFromEnv pre-filled the fields instead, an operator who
// only overrides SEP2_CA (not SEP2_SERVING_CA/SEP2_DEVICE_CA) would see the
// new settings silently frozen at whatever SEP2_CERT_DIR default was in
// effect at startup, not the SEP2_CA value they actually asked for.
func TestConfigFromEnvServingAndDeviceCAUnsetLeaveConfigEmpty(t *testing.T) {
	t.Setenv("SEP2_SERVING_CA", "")
	t.Setenv("SEP2_DEVICE_CA", "")
	if err := os.Unsetenv("SEP2_SERVING_CA"); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("SEP2_DEVICE_CA"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEP2_CA", "/etc/tls/ca.crt")

	cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
	if err != nil {
		t.Fatalf("configFromEnv: %v", err)
	}
	if cfg.ServingCAFile != "" {
		t.Errorf("ServingCAFile = %q, want empty (defaulting is EffectiveServingCA's job)", cfg.ServingCAFile)
	}
	if cfg.DeviceCAFile != "" {
		t.Errorf("DeviceCAFile = %q, want empty (defaulting is EffectiveDeviceCA's job)", cfg.DeviceCAFile)
	}
	if got := cfg.EffectiveServingCA(); got != "/etc/tls/ca.crt" {
		t.Errorf("EffectiveServingCA() = %q, want the SEP2_CA value /etc/tls/ca.crt", got)
	}
	if got := cfg.EffectiveDeviceCA(); got != "/etc/tls/ca.crt" {
		t.Errorf("EffectiveDeviceCA() = %q, want the SEP2_CA value /etc/tls/ca.crt", got)
	}
}

// TestConfigFromEnvServingAndDeviceCASetIndependently pins the "two
// different CAs" half of item 2 at the config-resolution layer: each env
// var wins over CAFile independently, tilde-expanded like every other
// path setting (#598).
func TestConfigFromEnvServingAndDeviceCASetIndependently(t *testing.T) {
	t.Setenv("SEP2_CA", "/etc/tls/ca.crt")
	t.Setenv("SEP2_SERVING_CA", "/etc/tls/serving.crt")
	t.Setenv("SEP2_DEVICE_CA", "/etc/tls/device.crt")

	cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
	if err != nil {
		t.Fatalf("configFromEnv: %v", err)
	}
	if got := cfg.EffectiveServingCA(); got != "/etc/tls/serving.crt" {
		t.Errorf("EffectiveServingCA() = %q, want /etc/tls/serving.crt", got)
	}
	if got := cfg.EffectiveDeviceCA(); got != "/etc/tls/device.crt" {
		t.Errorf("EffectiveDeviceCA() = %q, want /etc/tls/device.crt", got)
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

// TestConfigFromEnvAdminClientCA pins #657's test coverage finding: nothing
// exercised configFromEnv with SEP2_ADMIN_CLIENT_CA set, so a mutant that
// blanks AdminClientCA at its assignment (main.go, the AdminClientCA field
// literal) left every package green.
func TestConfigFromEnvAdminClientCA(t *testing.T) {
	t.Setenv("SEP2_ADMIN_CLIENT_CA", "/etc/tls/admin-client-ca.crt")

	cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
	if err != nil {
		t.Fatalf("configFromEnv: %v", err)
	}
	if got := cfg.AdminClientCA; got != "/etc/tls/admin-client-ca.crt" {
		t.Errorf("AdminClientCA = %q, want /etc/tls/admin-client-ca.crt", got)
	}
}

// TestConfigFromEnvAdminClientCASystemSentinelSurvivesHomeExpansion pins
// the main.go comment's claim that the "system" sentinel has no leading
// "~" and so passes through config.ExpandHome unchanged: nothing asserted
// it before, and a regression here would fail every host-roots deployment
// silently (envPathOr would try to resolve "system" as a home-relative
// path instead of leaving it alone).
func TestConfigFromEnvAdminClientCASystemSentinelSurvivesHomeExpansion(t *testing.T) {
	t.Setenv("SEP2_ADMIN_CLIENT_CA", config.AdminClientCASystemRoots)

	cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
	if err != nil {
		t.Fatalf("configFromEnv: %v", err)
	}
	if got := cfg.AdminClientCA; got != config.AdminClientCASystemRoots {
		t.Errorf("AdminClientCA = %q, want the sentinel %q unchanged", got, config.AdminClientCASystemRoots)
	}
}

// TestConfigFromEnvPEN pins #665: SEP2_PEN unset leaves Config.PEN nil (so
// FlowReservationResponse mRIDs mint fully random, per RouterConfig.PEN's
// contract), a valid base-10 uint32 parses through, and an unparseable
// value is a startup error rather than a silently ignored setting, matching
// this file's other fallible SEP2_* settings.
func TestConfigFromEnvPEN(t *testing.T) {
	t.Run("unset stays nil", func(t *testing.T) {
		t.Setenv("SEP2_PEN", "")
		if err := os.Unsetenv("SEP2_PEN"); err != nil {
			t.Fatalf("unset SEP2_PEN: %v", err)
		}
		cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
		if err != nil {
			t.Fatalf("configFromEnv: %v", err)
		}
		if cfg.PEN != nil {
			t.Errorf("PEN = %v, want nil", *cfg.PEN)
		}
	})

	t.Run("a valid value parses through", func(t *testing.T) {
		t.Setenv("SEP2_PEN", "1082533889") // 0x40832001

		cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
		if err != nil {
			t.Fatalf("configFromEnv: %v", err)
		}
		if cfg.PEN == nil || *cfg.PEN != 1082533889 {
			t.Errorf("PEN = %v, want 1082533889", cfg.PEN)
		}
	})

	t.Run("an unparseable value is a startup error", func(t *testing.T) {
		t.Setenv("SEP2_PEN", "not-a-number")

		if _, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"}); err == nil {
			t.Fatal("configFromEnv: want an error for an unparseable SEP2_PEN, got nil")
		}
	})

	t.Run("a value past uint32 range is a startup error", func(t *testing.T) {
		t.Setenv("SEP2_PEN", "4294967296") // 2^32

		if _, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"}); err == nil {
			t.Fatal("configFromEnv: want an error for a SEP2_PEN past uint32 range, got nil")
		}
	})

	// Fix round 3: the max uint32 value pins the ParseUint bit-size argument
	// itself. A mutant changing envUint32's `32` to `31` passed every test
	// above (none of them named a value only the 32nd bit distinguishes) and
	// would refuse this one.
	t.Run("the maximum uint32 value is accepted", func(t *testing.T) {
		t.Setenv("SEP2_PEN", "4294967295") // 2^32 - 1

		cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
		if err != nil {
			t.Fatalf("configFromEnv: unexpected error for the maximum uint32 value: %v", err)
		}
		if cfg.PEN == nil || *cfg.PEN != 4294967295 {
			t.Errorf("PEN = %v, want 4294967295", cfg.PEN)
		}
	})

	// "0" is a syntactically valid uint32, so envUint32 stores it as
	// configured; EffectivePEN is what treats it as unset, the same
	// normalization internal/dercontrol.Config already applies to its own
	// PEN field. Both halves are asserted so a regression in either layer is
	// caught at the layer it actually lives in.
	t.Run("0 parses through configFromEnv and is normalized to unset by EffectivePEN", func(t *testing.T) {
		t.Setenv("SEP2_PEN", "0")

		cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
		if err != nil {
			t.Fatalf("configFromEnv: %v", err)
		}
		if cfg.PEN == nil || *cfg.PEN != 0 {
			t.Errorf("PEN = %v, want a pointer to 0 (the raw env value, unnormalized)", cfg.PEN)
		}
		if got := cfg.EffectivePEN(); got != nil {
			t.Errorf("EffectivePEN() = %#x, want nil for SEP2_PEN=0 (IANA-reserved, treated as unset)", *got)
		}
	})

	t.Run("a negative value is a startup error", func(t *testing.T) {
		t.Setenv("SEP2_PEN", "-1")

		if _, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"}); err == nil {
			t.Fatal("configFromEnv: want an error for SEP2_PEN=-1, got nil")
		}
	})
}

// TestConfigFromEnvSEP2Edition pins #715 fix round 3 item 2: SEP2_EDITION
// unset leaves Config.SEP2Edition empty (EffectiveSEP2Edition then resolves
// "2018"), "2018" and "2023" both parse through unchanged, and an
// unrecognized value is a startup error, matching this file's other
// fallible SEP2_* settings (see TestConfigFromEnvPEN).
func TestConfigFromEnvSEP2Edition(t *testing.T) {
	t.Run("unset stays empty", func(t *testing.T) {
		t.Setenv("SEP2_EDITION", "")
		if err := os.Unsetenv("SEP2_EDITION"); err != nil {
			t.Fatalf("unset SEP2_EDITION: %v", err)
		}
		cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
		if err != nil {
			t.Fatalf("configFromEnv: %v", err)
		}
		if cfg.SEP2Edition != "" {
			t.Errorf("SEP2Edition = %q, want empty", cfg.SEP2Edition)
		}
		if got := cfg.EffectiveSEP2Edition(); got != "2018" {
			t.Errorf("EffectiveSEP2Edition() = %q, want \"2018\"", got)
		}
	})

	t.Run("2018 parses through", func(t *testing.T) {
		t.Setenv("SEP2_EDITION", "2018")
		cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
		if err != nil {
			t.Fatalf("configFromEnv: %v", err)
		}
		if cfg.SEP2Edition != "2018" {
			t.Errorf("SEP2Edition = %q, want \"2018\"", cfg.SEP2Edition)
		}
	})

	t.Run("2023 parses through", func(t *testing.T) {
		t.Setenv("SEP2_EDITION", "2023")
		cfg, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"})
		if err != nil {
			t.Fatalf("configFromEnv: %v", err)
		}
		if cfg.SEP2Edition != "2023" {
			t.Errorf("SEP2Edition = %q, want \"2023\"", cfg.SEP2Edition)
		}
	})

	t.Run("an unrecognized value is a startup error", func(t *testing.T) {
		t.Setenv("SEP2_EDITION", "2030")
		if _, err := configFromEnv(&certDirResolver{resolved: true, dir: "/test/certdir"}); err == nil {
			t.Fatal("configFromEnv: want an error for SEP2_EDITION=2030, got nil")
		}
	})
}

// #763: SEP2_FLOW_RESERVATION_DEADLINE_SECONDS reaches the config as a
// duration, and anything outside 1 to 3600 stops startup rather than being
// replaced by the default.
func TestConfigFromEnvFlowReservationDeadline(t *testing.T) {
	resolver := func() *certDirResolver { return &certDirResolver{resolved: true, dir: "/test/certdir"} }

	t.Run("a value in range is the hold", func(t *testing.T) {
		t.Setenv("SEP2_FLOW_RESERVATION_DEADLINE_SECONDS", "90")
		cfg, err := configFromEnv(resolver())
		if err != nil {
			t.Fatalf("configFromEnv: %v", err)
		}
		if cfg.FlowReservationDeadline != 90*time.Second {
			t.Errorf("FlowReservationDeadline = %v, want 1m30s", cfg.FlowReservationDeadline)
		}
	})

	t.Run("unset leaves the default to the effective accessor", func(t *testing.T) {
		t.Setenv("SEP2_FLOW_RESERVATION_DEADLINE_SECONDS", "")
		cfg, err := configFromEnv(resolver())
		if err != nil {
			t.Fatalf("configFromEnv: %v", err)
		}
		got, err := cfg.EffectiveFlowReservationDeadline()
		if err != nil || got != 300*time.Second {
			t.Errorf("EffectiveFlowReservationDeadline = %v, %v, want 5m0s", got, err)
		}
	})

	for _, bad := range []string{"0", "3601", "-1", "ten", "1.5"} {
		t.Run("invalid "+bad+" is a startup error", func(t *testing.T) {
			t.Setenv("SEP2_FLOW_RESERVATION_DEADLINE_SECONDS", bad)
			_, err := configFromEnv(resolver())
			if err == nil || !strings.Contains(err.Error(), "SEP2_FLOW_RESERVATION_DEADLINE_SECONDS") {
				t.Fatalf("configFromEnv = %v, want an error naming SEP2_FLOW_RESERVATION_DEADLINE_SECONDS", err)
			}
		})
	}
}
