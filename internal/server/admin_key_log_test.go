package server_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

// TestAdminStartupLogsHintButNeverTheKey boots the admin listener with a
// recognisable key from each source and scans everything the standard logger
// wrote, from boot through shutdown. The hint must name the sign-in URL and
// the key's source; no line may contain the key.
func TestAdminStartupLogsHintButNeverTheKey(t *testing.T) {
	const recognisable = "RECOGNISABLE-admin-key-7f3a91c2d4"

	cases := []struct {
		name      string
		source    string
		generated bool
		wantHint  string
	}{
		{"env source", "SEP2_ADMIN_KEY", false, "key from SEP2_ADMIN_KEY"},
		{"file source", "/var/lib/sep2/admin.secret", true, "key in /var/lib/sep2/admin.secret (generated now) (print it with: sep2server admin-key)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := teeLogOutput(t)
			_, adminPort, err := net.SplitHostPort(mustProbePort(t))
			if err != nil {
				t.Fatal(err)
			}
			bind := net.JoinHostPort("127.0.0.1", adminPort)

			c := newSplitListenerCerts(t)
			cfg := &config.Config{
				Addr:              c.sep2Probe,
				CertFile:          c.certFile,
				KeyFile:           c.keyFile,
				CAFile:            c.caFile,
				AdminListen:       bind,
				AdminKey:          recognisable,
				AdminKeySource:    tc.source,
				AdminKeyGenerated: tc.generated,
				TZOffset:          -28800,
				TimeQuality:       sep2.TimeQualityNTP,
			}

			ctx, cancel := context.WithCancel(context.Background())
			runErrCh := make(chan error, 1)
			go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()
			if msg, ok := waitForAdminReady(bind, false, adminReadyTimeout, runErrCh); !ok {
				cancel()
				t.Fatalf("%s on %s", msg, bind)
			}
			cancel()
			select {
			case <-runErrCh:
			case <-time.After(5 * time.Second):
				t.Fatal("server.Run did not exit within 5s after cancel")
			}

			out := buf.String()
			wantLine := "admin sign-in: http://" + bind + "/login, " + tc.wantHint
			if !strings.Contains(out, wantLine) {
				t.Errorf("log lacks the hint\nwant: %s\n---log---\n%s", wantLine, out)
			}
			if strings.Contains(out, recognisable) {
				t.Errorf("log contains the admin key\n---log---\n%s", out)
			}
			if strings.Contains(out, "Bearer auth disabled") {
				t.Errorf("log still carries the retired 'Bearer auth disabled' warning\n---log---\n%s", out)
			}
		})
	}
}

// TestAdminRefusalNeverContainsTheKey feeds a recognisable short key and
// checks the startup error and the log for it.
func TestAdminRefusalNeverContainsTheKey(t *testing.T) {
	const shortKey = "SHORT-KEY-9f2"
	buf := teeLogOutput(t)
	c := newSplitListenerCerts(t)
	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		AdminListen: "127.0.0.1:" + portOf(t, mustProbePort(t)),
		AdminKey:    shortKey,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}
	err := server.Run(context.Background(), cfg, c.svc)
	if err == nil {
		t.Fatal("server.Run = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "16") {
		t.Errorf("refusal does not name the floor: %v", err)
	}
	if strings.Contains(err.Error(), shortKey) || strings.Contains(buf.String(), shortKey) {
		t.Errorf("key leaked\nerror: %v\n---log---\n%s", err, buf.String())
	}
}

func portOf(t *testing.T, hostport string) string {
	t.Helper()
	_, p, err := net.SplitHostPort(hostport)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
