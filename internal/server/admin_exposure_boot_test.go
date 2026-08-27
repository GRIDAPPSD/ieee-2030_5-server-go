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

// exposureBootCfg builds a Run-ready config whose admin listener binds the
// unspecified IPv4 address on a port the caller can probe over loopback.
func exposureBootCfg(t *testing.T, adminPort string, allow bool) (*config.Config, *splitListenerCerts) {
	t.Helper()
	c := newSplitListenerCerts(t)
	return &config.Config{
		Addr:                  c.sep2Probe,
		CertFile:              c.certFile,
		KeyFile:               c.keyFile,
		CAFile:                c.caFile,
		AdminListen:           net.JoinHostPort("0.0.0.0", adminPort),
		AdminKey:              adminTestKey,
		AdminAllowNonLoopback: allow,
		TZOffset:              -28800,
		TimeQuality:           sep2.TimeQualityNTP,
	}, c
}

// bindable reports whether addr can be bound, which is the assertion the card
// asks for: a refusal must leave NO listening socket, and that is a property of
// the address rather than of the returned error. A successful bind proves the
// kernel holds no listener there; EADDRINUSE proves it holds one.
//
// A TCP dial cannot carry this assertion. On a WSL2 host a connect to a
// just-closed loopback port succeeds intermittently, so "dial refused" is not
// evidence of an absent listener. A bind is answered by the kernel that would
// have to accept, and it is deterministic in both directions.
func bindable(addr string) (bool, error) {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false, err
	}
	_ = l.Close()
	return true, nil
}

// TestAdminListenerRefusesNonLoopbackBindWithoutOptIn drives server.Run, the
// path production takes, and asserts the refusal is structural: Run returns an
// error naming the opt-in, and nothing is listening on the admin port.
//
// The opt-in case is the control. It proves the bind probe can observe an open
// socket on that exact address, so a successful bind in the refusal case means
// the socket was never opened rather than that the probe cannot see one.
func TestAdminListenerRefusesNonLoopbackBindWithoutOptIn(t *testing.T) {
	_, adminPort, err := net.SplitHostPort(mustProbePort(t))
	if err != nil {
		t.Fatalf("split probe port: %v", err)
	}
	loopbackProbe := net.JoinHostPort("127.0.0.1", adminPort)
	wildcardProbe := net.JoinHostPort("0.0.0.0", adminPort)

	if ok, err := bindable(wildcardProbe); !ok {
		t.Fatalf("port %s was already held before the test started: %v", adminPort, err)
	}

	t.Run("opt-in absent: refused with no socket", func(t *testing.T) {
		cfg, c := exposureBootCfg(t, adminPort, false)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runErrCh := make(chan error, 1)
		go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()

		select {
		case err := <-runErrCh:
			if err == nil {
				t.Fatal("server.Run returned nil, want a startup refusal")
			}
			for _, want := range []string{"SEP2_ADMIN_ALLOW_NON_LOOPBACK", cfg.AdminListen} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal missing %q\n---error---\n%v", want, err)
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server.Run did not refuse within 5s")
		}

		ok, bindErr := bindable(wildcardProbe)
		if !ok {
			t.Errorf("%s is still held after the refusal, so a socket was opened before the guard ran: %v",
				wildcardProbe, bindErr)
		}
	})

	t.Run("opt-in present: the same bind serves", func(t *testing.T) {
		cfg, c := exposureBootCfg(t, adminPort, true)

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
			if resp, err := probe.Get("http://" + loopbackProbe + "/api/certs/ca"); err == nil {
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

		ok, bindErr := bindable(wildcardProbe)
		if ok {
			t.Fatalf("%s was bindable with the opt-in set, so the bind probe has no positive control and the refusal case proves nothing",
				wildcardProbe)
		}
		if !strings.Contains(bindErr.Error(), "address already in use") {
			t.Errorf("bind probe failed for the wrong reason: %v", bindErr)
		}
	})
}

// TestAdminListenerRefusesWhitespaceOnlyAdminKey drives server.Run with a
// loopback admin bind, so the exposure guard cannot be what refuses, and
// asserts the whitespace-only key is a hard startup error that opens no socket
// and names the variable to fix. The unset case is the control: it must still
// boot, because unset is the deliberate "Bearer auth disabled" state.
func TestAdminListenerRefusesWhitespaceOnlyAdminKey(t *testing.T) {
	_, adminPort, err := net.SplitHostPort(mustProbePort(t))
	if err != nil {
		t.Fatalf("split probe port: %v", err)
	}
	loopbackBind := net.JoinHostPort("127.0.0.1", adminPort)

	newCfg := func(t *testing.T, key string) (*config.Config, *splitListenerCerts) {
		c := newSplitListenerCerts(t)
		return &config.Config{
			Addr:        c.sep2Probe,
			CertFile:    c.certFile,
			KeyFile:     c.keyFile,
			CAFile:      c.caFile,
			AdminListen: loopbackBind,
			AdminKey:    key,
			TZOffset:    -28800,
			TimeQuality: sep2.TimeQualityNTP,
		}, c
	}

	t.Run("whitespace-only key: refused with no socket", func(t *testing.T) {
		cfg, c := newCfg(t, " \t ")

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		runErrCh := make(chan error, 1)
		go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()

		select {
		case err := <-runErrCh:
			if err == nil {
				t.Fatal("server.Run returned nil, want a startup refusal")
			}
			if !strings.Contains(err.Error(), "SEP2_ADMIN_KEY") {
				t.Errorf("refusal does not name the env var\n---error---\n%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("server.Run did not refuse within 5s")
		}

		if ok, bindErr := bindable(loopbackBind); !ok {
			t.Errorf("%s is still held after the refusal, so a socket was opened before the guard ran: %v",
				loopbackBind, bindErr)
		}
	})

	t.Run("unset key: still boots with Bearer disabled", func(t *testing.T) {
		cfg, c := newCfg(t, "")

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

		if ok, _ := bindable(loopbackBind); ok {
			t.Fatalf("nothing listening on %s with the key unset; the refusal case has no positive control", loopbackBind)
		}
	})
}
