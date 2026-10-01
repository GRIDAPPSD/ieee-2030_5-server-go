package sep2server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/dercontrol"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// TestNewStores_EndDeviceDeleteLeavesNoCancelMark: an EndDevice delete served
// over the default stores removes the device's response lifecycle records
// with its responses, so a later device at the same key inherits no cancel
// mark (GRIDAPPSD/ieee-2030_5-server-go#761).
func TestNewStores_EndDeviceDeleteLeavesNoCancelMark(t *testing.T) {
	ctx := context.Background()
	s := NewStores()
	const lfdi = "AABBCCDDEEFF0011223344556677889900112233"
	if err := s.EndDevices.Create(ctx, "dev", sep2.EndDevice{LFDI: lfdi, SFDI: "AABBCCDD11223344"}); err != nil {
		t.Fatal(err)
	}
	frp := sep2.FlowReservationResponse{}
	frp.Href = "/edev/dev/frp/R1"
	frp.MRID = "MRID-R1"
	if err := s.FlowReservationResponses.Create(ctx, "dev", "R1", frp); err != nil {
		t.Fatal(err)
	}
	at := int64(777)
	if err := s.FlowReservationResponseLifecycles.Create(ctx, "dev", "R1", dercontrol.LifecycleRecord{CancelledAt: &at}); err != nil {
		t.Fatal(err)
	}
	// Control: the mark is there before the delete.
	if n, err := s.FlowReservationResponseLifecycles.Count(ctx, "dev"); err != nil || n != 1 {
		t.Fatalf("control: lifecycles under dev = %d, %v, want 1", n, err)
	}

	policy := assembly.AuthPolicy{
		Wrap:       func(h http.Handler) http.Handler { return h },
		Identity:   func(context.Context) (string, string, bool) { return lfdi, "AABBCCDD11223344", true },
		SFDIPrefix: func(sfdi string) (string, error) { return sfdi[:8], nil },
	}
	router, _ := assembly.BuildProtocolRouter(assembly.RouterConfig{}, s, policy, "serverSFDI", "serverLFDI", nil)
	srv := httptest.NewServer(router)
	defer srv.Close()
	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close() // only the status is read
	if resp.StatusCode/100 != 2 {
		t.Fatalf("DELETE /edev/dev = %d, want 2xx", resp.StatusCode)
	}
	if n, err := s.FlowReservationResponses.Count(ctx, "dev"); err != nil || n != 0 {
		t.Errorf("responses under dev = %d, %v, want 0", n, err)
	}
	if n, err := s.FlowReservationResponseLifecycles.Count(ctx, "dev"); err != nil || n != 0 {
		t.Errorf("lifecycles under dev = %d, %v, want 0", n, err)
	}
}
