package assembly_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// TestEndDeviceDelete_CascadesFlowReservationAndLogEventRecords is the
// end-to-end pin for GRIDAPPSD/ieee-2030_5-server-go#701: DELETE /edev/{id}
// must not leave the device's flow reservation and log event records behind
// under the dead key, where a device later created at the same key would
// inherit them.
func TestEndDeviceDelete_CascadesFlowReservationAndLogEventRecords(t *testing.T) {
	t.Parallel()

	stores := testStores()
	seedOwnedDevices(t, stores.EndDevices, "e1")
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		testSFDI, testLFDI,
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	postFlowReservationRequest(t, srv, "e1", "6162636465666768696A6B6C6D6E6F70")
	postLogEvent(t, srv, "e1", sampleLogEvent(1, 27))

	ctx := context.Background()

	// Control: the records exist under "e1" before the delete, so the zero
	// counts asserted below mean the cascade ran rather than nothing having
	// been seeded.
	if n, err := stores.FlowReservationRequests.Count(ctx, "e1"); err != nil || n != 1 {
		t.Fatalf("control: FlowReservationRequests under e1 = %d, %v, want 1, nil", n, err)
	}
	if n, err := stores.FlowReservationResponses.Count(ctx, "e1"); err != nil || n != 1 {
		t.Fatalf("control: FlowReservationResponses under e1 = %d, %v, want 1, nil", n, err)
	}
	if n, err := stores.LogEvents.Count(ctx, "e1"); err != nil || n != 1 {
		t.Fatalf("control: LogEvents under e1 = %d, %v, want 1, nil", n, err)
	}

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/e1", nil)
	if err != nil {
		t.Fatalf("new DELETE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /edev/e1: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE /edev/e1 status = %d, want 204", resp.StatusCode)
	}

	if n, err := stores.FlowReservationRequests.Count(ctx, "e1"); err != nil || n != 0 {
		t.Errorf("FlowReservationRequests under the dead key e1 = %d, %v, want 0, nil", n, err)
	}
	if n, err := stores.FlowReservationResponses.Count(ctx, "e1"); err != nil || n != 0 {
		t.Errorf("FlowReservationResponses under the dead key e1 = %d, %v, want 0, nil", n, err)
	}
	if n, err := stores.LogEvents.Count(ctx, "e1"); err != nil || n != 0 {
		t.Errorf("LogEvents under the dead key e1 = %d, %v, want 0, nil", n, err)
	}

	// The issue's own probe: seed a new device at the same key (same caller
	// identity, so the ownership gate still admits it) and confirm it is not
	// served the dead device's records through its own advertised links.
	newDev := sep2.EndDevice{SFDI: "9999999999", LFDI: testLFDI}
	newDev.Href = "/edev/e1"
	if err := stores.EndDevices.Create(ctx, "e1", newDev); err != nil {
		t.Fatalf("recreate device at the reused key: %v", err)
	}

	_, frpBody := getBytes(t, srv, "/edev/e1/frp")
	var frpList sep2.FlowReservationResponseList
	if err := xml.Unmarshal(frpBody, &frpList); err != nil {
		t.Fatalf("unmarshal FlowReservationResponseList: %v", err)
	}
	if len(frpList.FlowReservationResponse) != 0 {
		t.Errorf("the reused key e1 was served %d flow reservation response(s) from the dead device, want 0",
			len(frpList.FlowReservationResponse))
	}

	_, lelBody := getBytes(t, srv, "/edev/e1/lel")
	var lelList sep2.LogEventList
	if err := xml.Unmarshal(lelBody, &lelList); err != nil {
		t.Fatalf("unmarshal LogEventList: %v", err)
	}
	if len(lelList.LogEvent) != 0 {
		t.Errorf("the reused key e1 was served %d log event(s) from the dead device, want 0", len(lelList.LogEvent))
	}
}

// TestEndDeviceDelete_FailsClosedWhenFlowReservationResponsesIsMiswired
// covers the half-wired family case miswired.go documents: FlowReservationRequests
// is present but its sibling FlowReservationResponses is not. The frq/frp
// routes already answer 500 rather than panic in that state (miswired.go);
// DELETE /edev/{id} must do the same rather than delete the device while
// unable to cascade its (possibly present) response records.
func TestEndDeviceDelete_FailsClosedWhenFlowReservationResponsesIsMiswired(t *testing.T) {
	t.Parallel()

	stores := testStores()
	stores.FlowReservationResponses = nil
	seedOwnedDevices(t, stores.EndDevices, "e1")
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{},
		stores,
		testAuthPolicy(),
		testSFDI, testLFDI,
		nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodDelete, srv.URL+"/edev/e1", nil)
	if err != nil {
		t.Fatalf("new DELETE request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE /edev/e1: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("DELETE /edev/e1 status = %d, want 500: a half-wired flow reservation family must refuse, not silently delete", resp.StatusCode)
	}

	if _, err := stores.EndDevices.Get(context.Background(), "e1"); err != nil {
		t.Errorf("device was removed despite the failed cascade: Get(e1) = %v, want the device still present", err)
	}
}
