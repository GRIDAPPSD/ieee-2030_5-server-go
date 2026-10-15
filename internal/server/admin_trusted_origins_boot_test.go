package server_test

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

const trustedOriginBootValue = "http://trusted.example:18444"

func trustedOriginsBootCfg(t *testing.T, loopbackBind, trusted string) (*config.Config, *splitListenerCerts) {
	t.Helper()
	c := newSplitListenerCerts(t)
	return &config.Config{
		Addr:                c.sep2Probe,
		CertFile:            c.certFile,
		KeyFile:             c.keyFile,
		CAFile:              c.caFile,
		AdminListen:         loopbackBind,
		AdminKey:            adminTestKey,
		AdminTrustedOrigins: trusted,
		TZOffset:            -28800,
		TimeQuality:         sep2.TimeQualityNTP,
	}, c
}

// TestAdminTrustedOriginsReachTheServedRouter drives server.Run, so it covers
// the hand-off from the env value to the router that a unit test of the
// parser cannot: a setting parsed and then dropped would still pass those.
func TestAdminTrustedOriginsReachTheServedRouter(t *testing.T) {
	_, adminPort, err := net.SplitHostPort(mustProbePort(t))
	if err != nil {
		t.Fatalf("split probe port: %v", err)
	}
	loopbackBind := net.JoinHostPort("127.0.0.1", adminPort)

	t.Run("trusted origin passes and another is refused", func(t *testing.T) {
		cfg, c := trustedOriginsBootCfg(t, loopbackBind, trustedOriginBootValue)

		ctx, cancel := context.WithCancel(context.Background())
		runErrCh := make(chan error, 1)
		go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()
		t.Cleanup(func() {
			cancel()
			select {
			case <-runErrCh:
			case <-time.After(5 * time.Second):
				t.Error("server.Run did not exit within 5s after cancel")
			}
		})

		probe := &http.Client{Timeout: 500 * time.Millisecond}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if resp, err := probe.Get("http://" + loopbackBind + "/api/certs/ca"); err == nil {
				_ = resp.Body.Close()
				break
			}
			select {
			case err := <-runErrCh:
				t.Fatalf("server.Run exited during boot: %v", err)
			default:
			}
			time.Sleep(25 * time.Millisecond)
		}

		post := func(origin string) (int, string) {
			code, _, body := doAdmin(t, http.MethodPost, "http://"+loopbackBind+"/api/fsas", `{}`, map[string]string{
				"Content-Type":   "application/json",
				"Authorization":  "Bearer " + adminTestKey,
				"Sec-Fetch-Site": "cross-site",
				"Origin":         origin,
			})
			return code, body
		}

		if code, body := post("http://attacker.example"); code != http.StatusForbidden || body != crossOriginRefusalBody {
			t.Errorf("untrusted origin: status %d body %q, want 403 %q", code, body, crossOriginRefusalBody)
		}
		// The handler's own 400 shows the request cleared the cross-origin
		// check and the credential guard; a 401 or 403 would not.
		if code, body := post(trustedOriginBootValue); code != http.StatusBadRequest || !strings.Contains(body, "description required") {
			t.Errorf("trusted origin: status %d body %q, want the handler's 400 description required", code, body)
		}
	})

	t.Run("malformed value refuses startup naming the setting", func(t *testing.T) {
		cfg, c := trustedOriginsBootCfg(t, loopbackBind, "not an origin")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runErrCh := make(chan error, 1)
		go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()

		select {
		case err := <-runErrCh:
			if err == nil {
				t.Fatal("server.Run returned nil, want a startup refusal")
			}
			if !strings.Contains(err.Error(), "SEP2_ADMIN_TRUSTED_ORIGINS") {
				t.Errorf("refusal does not name the setting\n---error---\n%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server.Run did not refuse within 5s")
		}

		if ok, bindErr := bindable(loopbackBind); !ok {
			t.Errorf("%s is still held after the refusal: %v", loopbackBind, bindErr)
		}
	})
}
