package server_test

// #440 item 6: a new pair takes effect on the next protocol request, and a
// removed pair revokes on the next request. Asserted end to end through the
// protocol listener, over a real socket presenting a real client
// certificate, rather than by reading the management store back: reading
// the store back would only prove Assign/Unassign work, which the store's
// own unit tests already cover, not that the admin-plane pair is the same
// shape the ownership gate at pkg/sep2srv/assembly/ownership.go actually
// consults.

import (
	"context"
	"crypto/tls"
	"encoding/json"
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

// managementE2EDevice is one generated device identity: its client TLS
// config (for driving protocol requests as that device) plus the LFDI/SFDI
// derived from its own leaf certificate, the same way admin_register.go's
// HandleCertInfo derives them.
type managementE2EDevice struct {
	lfdi      string
	sfdi      string
	tlsConfig *tls.Config
}

func newManagementE2EDevice(t *testing.T, c *splitListenerCerts, serial string) managementE2EDevice {
	t.Helper()
	caCert, _, err := certs.LoadCA(c.caFile, c.caKeyFile)
	if err != nil {
		t.Fatalf("LoadCA: %v", err)
	}
	caKey, err := certs.ParseKeyPEM(c.caKeyPEM)
	if err != nil {
		t.Fatalf("ParseKeyPEM(ca): %v", err)
	}
	certPEM, keyPEM, err := certs.GenerateDeviceCert(caCert, caKey, certs.DeviceCertOptions{
		DeviceType:  certs.DeviceTypeGeneric,
		HWSerialNum: serial,
	})
	if err != nil {
		t.Fatalf("GenerateDeviceCert(%s): %v", serial, err)
	}
	leaf, err := certs.ParseCertificatePEM(certPEM)
	if err != nil {
		t.Fatalf("ParseCertificatePEM(%s): %v", serial, err)
	}
	tlsCfg, err := sepTLS.NewClientTLSConfigFromPEM(certPEM, keyPEM, c.caCertPEM)
	if err != nil {
		t.Fatalf("NewClientTLSConfigFromPEM(%s): %v", serial, err)
	}
	return managementE2EDevice{
		lfdi:      sepTLS.LFDI(leaf),
		sfdi:      sepTLS.SFDI(leaf),
		tlsConfig: tlsCfg,
	}
}

// TestManagementPairTakesEffectAndRevokesOverTheProtocolListener is the
// item 6 evidence: create the pair through the admin API, prove the
// aggregator's NEXT protocol request against the managed device succeeds;
// remove the pair through the admin API, prove the aggregator's NEXT
// protocol request is refused again. Both directions run against a real
// TLS socket with a real client certificate.
func TestManagementPairTakesEffectAndRevokesOverTheProtocolListener(t *testing.T) {
	t.Parallel()
	c := newSplitListenerCerts(t)
	manager := newManagementE2EDevice(t, c, "mgmt-e2e-manager")
	child := newManagementE2EDevice(t, c, "mgmt-e2e-child")

	cfg := &config.Config{
		Addr:        c.sep2Probe,
		CertFile:    c.certFile,
		KeyFile:     c.keyFile,
		CAFile:      c.caFile,
		AdminListen: c.adminProbe,
		AdminKey:    adminTestKey,
		TZOffset:    -28800,
		TimeQuality: sep2.TimeQualityNTP,
	}
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

	if !waitForServerReady(c.sep2Probe, 3*time.Second, child.tlsConfig) {
		t.Fatal("SEP2 listener never became ready")
	}
	adminClient := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, err := adminClient.Get("http://" + c.adminProbe + "/api/certs/ca")
		if err == nil {
			_ = resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("admin listener never became ready")
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Register the child as an EndDevice through the admin plane: the gate
	// answers 404 before it ever reaches the self/management check for an
	// id with no record, so a pair alone proves nothing without this.
	childHref := adminMgmtRegisterDevice(t, adminClient, c.adminProbe, child.sfdi, child.lfdi)

	managerClient := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{TLSClientConfig: manager.tlsConfig}}
	protoURL := "https://" + c.sep2Probe + childHref

	// Before any pair exists, the aggregator is an ordinary, unrelated
	// device: refused.
	if code := adminMgmtProtoGET(t, managerClient, protoURL); code != http.StatusForbidden {
		t.Fatalf("GET %s before any pair: status = %d, want 403", protoURL, code)
	}

	// Create the pair through the admin plane.
	adminMgmtCreatePair(t, adminClient, c.adminProbe, manager.lfdi, child.lfdi)

	// The NEXT request succeeds: the pair took effect with no restart and
	// no cache to invalidate.
	if code := adminMgmtProtoGET(t, managerClient, protoURL); code != http.StatusOK {
		t.Fatalf("GET %s after create: status = %d, want 200 (pair should have taken effect)", protoURL, code)
	}

	// Remove the pair through the admin plane.
	adminMgmtRemovePair(t, adminClient, c.adminProbe, child.lfdi)

	// The NEXT request is refused again: revocation took effect immediately.
	if code := adminMgmtProtoGET(t, managerClient, protoURL); code != http.StatusForbidden {
		t.Fatalf("GET %s after remove: status = %d, want 403 (removal should have revoked)", protoURL, code)
	}
}

func adminMgmtProtoGET(t *testing.T, client *http.Client, url string) int {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func adminMgmtRegisterDevice(t *testing.T, client *http.Client, adminAddr, sfdi, lfdi string) string {
	t.Helper()
	body := `{"sfdi":"` + sfdi + `","lfdi":"` + lfdi + `","enabled":true,"pin":123456}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+adminAddr+"/api/devices", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminTestKey)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/devices: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/devices: status = %d, body = %s", resp.StatusCode, respBody)
	}
	var got struct {
		Href string `json:"href"`
	}
	if err := json.Unmarshal(respBody, &got); err != nil || got.Href == "" {
		t.Fatalf("POST /api/devices: body = %s, decode err = %v", respBody, err)
	}
	return got.Href
}

func adminMgmtCreatePair(t *testing.T, client *http.Client, adminAddr, managerLFDI, managedLFDI string) {
	t.Helper()
	body := `{"managerLFDI":"` + managerLFDI + `","managedLFDI":"` + managedLFDI + `"}`
	req, _ := http.NewRequest(http.MethodPost, "http://"+adminAddr+"/api/management-pairs", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+adminTestKey)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /api/management-pairs: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /api/management-pairs: status = %d, body = %s", resp.StatusCode, respBody)
	}
}

func adminMgmtRemovePair(t *testing.T, client *http.Client, adminAddr, managedLFDI string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, "http://"+adminAddr+"/api/management-pairs?managed="+managedLFDI, nil)
	req.Header.Set("Authorization", "Bearer "+adminTestKey)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("DELETE /api/management-pairs: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("DELETE /api/management-pairs: status = %d, body = %s", resp.StatusCode, body)
	}
}
