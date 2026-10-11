package server

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	saved := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(saved) })
	return &buf
}

func TestAdminCrossOriginPolicyWarnsOnlyWhenDisabled(t *testing.T) {
	for _, tc := range []struct {
		raw      string
		wantWarn bool
	}{
		{"*", true},
		{"", false},
		{"http://192.168.177.10:18444", false},
	} {
		buf := captureSlog(t)
		if _, err := adminCrossOriginPolicy(tc.raw); err != nil {
			t.Fatalf("adminCrossOriginPolicy(%q): %v", tc.raw, err)
		}
		out := buf.String()
		got := strings.Contains(out, "level=WARN") && strings.Contains(out, "cross-origin protection is disabled for the admin plane")
		if got != tc.wantWarn {
			t.Errorf("value %q: disabled warning = %v, want %v; log: %s", tc.raw, got, tc.wantWarn, out)
		}
	}
}

func TestAdminCrossOriginPolicyRefusesMalformed(t *testing.T) {
	buf := captureSlog(t)
	for _, raw := range []string{"not-an-origin", "*,http://a.example"} {
		if p, err := adminCrossOriginPolicy(raw); err == nil {
			t.Errorf("adminCrossOriginPolicy(%q) = %+v, nil; want an error", raw, p)
		}
	}
	if buf.Len() != 0 {
		t.Errorf("a refused value logged: %s", buf)
	}
}
