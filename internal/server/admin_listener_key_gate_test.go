package server_test

// #638 fix round 3 item 1: the admin listener's own start gate must not
// widen just because the banner learned to read a keyless certificate (fix
// round 1). Before the split, a missing or unparseable CA key made LoadCA
// fail outright, both certificates stayed nil, svc stayed nil, and
// server.Run's gate at server.go:421 left the admin listener down.
// LoadCAPair (round 1) returns a certificate independently of its key, so
// svc is non-nil whenever EITHER certificate loaded - and until this fix
// that alone started the whole admin plane: login, the UI, and every admin
// route, not only the CA download round 1 set out to enable. This proves
// the gate by the security lane's own method: boot twice, differing only
// in which key is present, and probe the admin port.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

func TestAdminListenerRequiresAUsableKeyNotJustACertificate(t *testing.T) {
	c := newSplitListenerCerts(t)
	caCert, err := certs.ParseCertificatePEM(c.caCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(c.caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM: %v", err)
	}

	tests := []struct {
		name       string
		svc        *handler.AdminCertService
		wantListen bool
	}{
		{
			name:       "cert only on both roles, no key: listener must not start",
			svc:        handler.NewAdminCertServiceWithCAs(caCert, nil, c.caCertPEM, caCert, nil),
			wantListen: false,
		},
		{
			name:       "serving key present, device key absent: listener starts",
			svc:        handler.NewAdminCertServiceWithCAs(caCert, caKey, c.caCertPEM, caCert, nil),
			wantListen: true,
		},
		{
			name:       "device key present, serving key absent: listener starts",
			svc:        handler.NewAdminCertServiceWithCAs(caCert, nil, c.caCertPEM, caCert, caKey),
			wantListen: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sep2Addr := mustProbePort(t)
			adminAddr := mustProbePort(t)
			cfg := &config.Config{
				Addr:        sep2Addr,
				CertFile:    c.certFile,
				KeyFile:     c.keyFile,
				CAFile:      c.caFile,
				AdminListen: adminAddr,
				AdminKey:    adminTestKey,
				TZOffset:    -28800,
				TimeQuality: sep2.TimeQualityNTP,
			}

			ctx, cancel := context.WithCancel(context.Background())
			runErrCh := make(chan error, 1)
			go func() { runErrCh <- server.Run(ctx, cfg, tt.svc) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-runErrCh:
				case <-time.After(3 * time.Second):
					t.Error("server.Run did not exit within 3s after cancel")
				}
			})

			if tt.wantListen {
				probe := &http.Client{Timeout: 500 * time.Millisecond}
				deadline := time.Now().Add(3 * time.Second)
				reached := false
				for time.Now().Before(deadline) {
					if resp, err := probe.Get("http://" + adminAddr + "/api/certs/ca"); err == nil {
						_ = resp.Body.Close()
						reached = true
						break
					}
					select {
					case err := <-runErrCh:
						t.Fatalf("server.Run exited during boot: %v", err)
					default:
					}
					time.Sleep(25 * time.Millisecond)
				}
				if !reached {
					t.Fatalf("admin listener at %s never answered; want it to start with a usable key", adminAddr)
				}
				return
			}

			// wantListen == false: give Run every chance to have bound the
			// port, then assert the kernel holds no listener there. bindable
			// is the same instrument admin_startup_gates_boot_test.go uses:
			// a bind is answered by the kernel that would have to accept,
			// deterministic in both directions, unlike a dial on a loopback
			// port that can succeed intermittently after a close.
			time.Sleep(300 * time.Millisecond)
			select {
			case err := <-runErrCh:
				t.Fatalf("server.Run exited early: %v", err)
			default:
			}
			if ok, bindErr := bindable(adminAddr); !ok {
				t.Errorf("%s is held with only a certificate loaded (no key on either role); admin plane started when it must not: %v", adminAddr, bindErr)
			}
		})
	}
}
