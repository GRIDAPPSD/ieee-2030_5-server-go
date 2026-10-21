package server_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
	coresub "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/subscription"
)

const loopbackOptInWarning = "WARNING: SEP2_NOTIFICATION_ALLOW_LOOPBACK=true"

// bootRunForNotificationPolicy runs server.Run with only the protocol
// listener and returns its address and an mTLS client for it. Run is
// cancelled and awaited when t ends.
func bootRunForNotificationPolicy(t *testing.T, allowLoopback bool) (string, *http.Client) {
	t.Helper()
	addr, client, cancel, runErr := startRunForNotificationPolicy(t, allowLoopback)
	t.Cleanup(func() {
		client.CloseIdleConnections()
		cancel()
		select {
		case <-runErr:
		case <-time.After(5 * time.Second):
			t.Error("server.Run did not exit within 5s after cancel")
		}
	})
	return addr, client
}

// startRunForNotificationPolicy is bootRunForNotificationPolicy without the
// cleanup: the caller cancels Run and reads its result.
func startRunForNotificationPolicy(t *testing.T, allowLoopback bool) (string, *http.Client, context.CancelFunc, <-chan error) {
	t.Helper()

	c := newSplitListenerCerts(t)
	caCert, _, err := certs.LoadCA(c.caFile, c.caKeyFile)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(c.caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(ca): %v", err)
	}
	deviceCertPEM, deviceKeyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: "notification-policy",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert: %v", err)
	}
	clientTLSCfg := ccmClientTLSConfig(t, deviceCertPEM, deviceKeyPEM, c.caCertPEM)

	// Every /edev/{id} route admits only the EndDevice's owner, so the boot
	// fixture seeds edev-1 with this client certificate's identity.
	deviceCert, err := certs.ParseCertificatePEM(deviceCertPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(device): %v", err)
	}
	fixture := filepath.Join(t.TempDir(), "notification-policy-edev.yaml")
	fixtureYAML := fmt.Sprintf("end_devices:\n  - id: %q\n    sfdi: %q\n    lfdi: %q\n",
		"edev-1", sepTLS.SFDI(deviceCert), sepTLS.LFDI(deviceCert))
	if err := os.WriteFile(fixture, []byte(fixtureYAML), 0o600); err != nil {
		t.Fatalf("write boot fixture: %v", err)
	}

	run := startRun(t, func(addr string) *config.Config {
		return &config.Config{
			Addr:                      addr,
			CertFile:                  c.certFile,
			KeyFile:                   c.keyFile,
			CAFile:                    c.caFile,
			TZOffset:                  -28800,
			TimeQuality:               sep2.TimeQualityNTP,
			NotificationAllowLoopback: allowLoopback,
			BootFixtureFile:           fixture,
		}
	}, c.svc, clientTLSCfg)
	return run.addr, ccmHTTPClient(clientTLSCfg, 5*time.Second), run.cancel, run.runErrCh
}

// Not parallel: it swaps the process-wide log output.
func TestRunAppliesNotificationLoopbackPolicy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		allow       bool
		wantStatus  int
		wantStored  int
		wantWarning bool
	}{
		{"default config refuses loopback", false, http.StatusBadRequest, 0, false},
		{"opt-in stores loopback and warns", true, http.StatusCreated, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureServerLog(t)
			addr, client := bootRunForNotificationPolicy(t, tc.allow)
			rcv := newLoopbackReceiver(t)
			base := "https://" + addr + "/edev/edev-1/sub"

			body := []byte(`<Subscription xmlns="urn:ieee:std:2030.5:ns">` +
				`<subscribedResource>/edev/edev-1</subscribedResource>` +
				`<notificationURI>` + rcv.uri + `</notificationURI>` +
				`<encoding>0</encoding>` +
				`</Subscription>`)
			resp, err := client.Post(base, "application/sep+xml", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("POST %s: %v", base, err)
			}
			respBody, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("POST status = %d, want %d; body=%s", resp.StatusCode, tc.wantStatus, respBody)
			}

			listResp, err := client.Get(base + "?l=255")
			if err != nil {
				t.Fatalf("GET %s: %v", base, err)
			}
			listBody, _ := io.ReadAll(listResp.Body)
			_ = listResp.Body.Close()
			if listResp.StatusCode != http.StatusOK {
				t.Fatalf("GET status = %d, want 200; body=%s", listResp.StatusCode, listBody)
			}
			var list sep2.SubscriptionList
			if err := xml.Unmarshal(listBody, &list); err != nil {
				t.Fatalf("unmarshal SubscriptionList: %v\n%s", err, listBody)
			}
			if len(list.Subscription) != tc.wantStored {
				t.Errorf("stored subscriptions = %d, want %d", len(list.Subscription), tc.wantStored)
			}
			if tc.wantStored == 1 && list.Subscription[0].NotificationURI != rcv.uri {
				t.Errorf("stored NotificationURI = %q, want %q", list.Subscription[0].NotificationURI, rcv.uri)
			}
			if got := strings.Contains(logs.String(), loopbackOptInWarning); got != tc.wantWarning {
				t.Errorf("startup warning logged = %v, want %v", got, tc.wantWarning)
			}
		})
	}
}

// Run must not return while the notification manager is still shutting
// down: the drops it counts and logs then could be lost when the process
// exits right after Run.
//
// Not parallel: it replaces how Run starts the notification manager.
func TestRunWaitsForNotificationManager(t *testing.T) {
	managerReturned := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseManager := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseManager)
	server.SetStartNotifier(t, func(m *coresub.Manager, ctx context.Context) {
		m.Start(ctx)
		close(managerReturned)
		<-release
	})

	_, client, cancel, runErr := startRunForNotificationPolicy(t, false)
	t.Cleanup(cancel)
	client.CloseIdleConnections()
	cancel()

	select {
	case <-managerReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("the notification manager did not return within 5s of cancel")
	}
	select {
	case err := <-runErr:
		t.Fatalf("server.Run returned (err %v) while the notification manager was still shutting down", err)
	case <-time.After(200 * time.Millisecond):
	}

	releaseManager()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("server.Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server.Run did not return within 5s after the notification manager finished")
	}
}
