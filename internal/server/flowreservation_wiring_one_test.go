package server

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
)

// A deadline outside the setting's range stops the stores from being built,
// so Run never starts a queue with a hold the env parser would have refused.
func TestNewRunStores_RefusesADeadlineOutsideTheSettingsRange(t *testing.T) {
	for _, d := range []time.Duration{-time.Second, time.Second - 1, time.Hour + 1} {
		if _, _, err := newRunStores(&config.Config{FlowReservationDeadline: d}); err == nil {
			t.Errorf("newRunStores with deadline %v: nil error, want a refusal", d)
		}
	}
	s, _, err := newRunStores(&config.Config{FlowReservationDeadline: 45 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if s.FlowReservationDeadline != 45*time.Second {
		t.Errorf("FlowReservationDeadline = %v, want 45s", s.FlowReservationDeadline)
	}
	s, _, err = newRunStores(&config.Config{})
	if err != nil || s.FlowReservationDeadline != 300*time.Second {
		t.Errorf("unset: FlowReservationDeadline = %v, %v, want 5m0s", s.FlowReservationDeadline, err)
	}
}

// The process has one DER control issuer: the stores hold it, the admin
// handler uses it, and the assembly receives it.
func TestNewRunStores_OneIssuerReachesEveryConsumer(t *testing.T) {
	s, _, err := newRunStores(&config.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if s.DERControlIssuer == nil {
		t.Fatal("DERControlIssuer = nil after newRunStores")
	}
	h := newAdminDERControlHandler(s)
	if h == nil {
		t.Fatal("no admin DER control handler")
	}
	if h.Issuer != s.DERControlIssuer {
		t.Errorf("admin handler issuer = %p, want the stores' %p", h.Issuer, s.DERControlIssuer)
	}
	if got := NewCoreStores(s).DERControlIssuer; got != s.DERControlIssuer {
		t.Errorf("assembly issuer = %p, want the stores' %p", got, s.DERControlIssuer)
	}
}

// The queue the stores carry is the one the POST route submits to: the
// router's own deadline is an hour, and only the supplied queue's 20 ms hold
// can answer inside the test.
func TestAssembly_UsesTheQueueTheStoresCarry(t *testing.T) {
	const lfdi = "AABBCCDDEEFF0011223344556677889900112233"
	s, _, err := newRunStores(&config.Config{FlowReservationDeadline: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EndDevices.Create(context.Background(), "dev", sep2.EndDevice{LFDI: lfdi}); err != nil {
		t.Fatal(err)
	}
	s.FlowReservationDeadline = 20 * time.Millisecond
	s.FlowReservationQueue = newFlowReservationQueue(s, nil)
	t.Cleanup(s.FlowReservationQueue.Close)

	policy := assembly.AuthPolicy{
		Wrap:       func(h http.Handler) http.Handler { return h },
		Identity:   func(context.Context) (string, string, bool) { return lfdi, "AABBCCDD11223344", true },
		SFDIPrefix: func(sfdi string) (string, error) { return sfdi[:8], nil },
	}
	protocol, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{FlowReservationDeadline: time.Hour},
		NewCoreStores(s), policy, "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(protocol)
	t.Cleanup(srv.Close)

	duration := uint16(600)
	body, err := xml.Marshal(&sep2.FlowReservationRequest{MRID: "0102030405060708090A0B0C0D0E0F10", DurationRequested: &duration})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(srv.URL+"/edev/dev/frq", "application/sep+xml", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST status = %d, want 201", resp.StatusCode)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		page, err := s.FlowReservationResponses.List(context.Background(), "dev", store.ListOptions{Unbounded: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Items) == 1 {
			if got := page.Items[0].Subject; got != "0102030405060708090A0B0C0D0E0F10" {
				t.Errorf("response Subject = %q, want the request's mRID", got)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no response stored within 3s: the route submitted to a queue other than the one the stores carry")
}
