package server_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	sepTLS "github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2tls"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/certs"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/server"
)

const loopbackOptInWarning = "WARNING: SEP2_NOTIFICATION_ALLOW_LOOPBACK=true"

// bootRunForNotificationPolicy runs server.Run with only the protocol
// listener and returns its address and an mTLS client for it.
func bootRunForNotificationPolicy(t *testing.T, allowLoopback bool) (string, *http.Client) {
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
	clientTLSCfg, err := sepTLS.NewClientTLSConfigFromPEM(deviceCertPEM, deviceKeyPEM, c.caCertPEM)
	if err != nil {
		t.Fatalf("NewClientTLSConfigFromPEM: %v", err)
	}

	cfg := &config.Config{
		Addr:                      c.sep2Probe,
		CertFile:                  c.certFile,
		KeyFile:                   c.keyFile,
		CAFile:                    c.caFile,
		TZOffset:                  -28800,
		TimeQuality:               sep2.TimeQualityNTP,
		NotificationAllowLoopback: allowLoopback,
	}
	ctx, cancel := context.WithCancel(context.Background())
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- server.Run(ctx, cfg, c.svc) }()

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLSCfg}, Timeout: 5 * time.Second}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		cancel()
		select {
		case <-runErrCh:
		case <-time.After(5 * time.Second):
			t.Error("server.Run did not exit within 5s after cancel")
		}
	})
	if !waitForServerReady(c.sep2Probe, 5*time.Second, clientTLSCfg) {
		select {
		case err := <-runErrCh:
			t.Fatalf("server.Run exited before the protocol listener was ready: %v", err)
		default:
			t.Fatalf("protocol listener never became ready on %s", c.sep2Probe)
		}
	}
	return c.sep2Probe, client
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
