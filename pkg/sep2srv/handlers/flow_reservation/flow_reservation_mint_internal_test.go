package flow_reservation

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// TestHandlePostFlowReservationRequest_MintFailureStoresNothing is a
// whitebox test (package flow_reservation, not _test) because it needs
// frpRandRead to force the failure.
//
// The mint must happen before any store write: a mint failure after the
// request is already stored would leave an orphaned FlowReservationRequest
// behind a 500, with no response and no way for the client to tell the
// request was ever accepted.
func TestHandlePostFlowReservationRequest_MintFailureStoresNothing(t *testing.T) {
	orig := frpRandRead
	defer func() { frpRandRead = orig }()
	frpRandRead = func(b []byte) (int, error) { return 0, errors.New("boom") }

	frqStore := memory.NewScopedStore[sep2.FlowReservationRequest]()
	frpStore := memory.NewScopedStore[sep2.FlowReservationResponse]()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /edev/{id}/frq", HandlePostFlowReservationRequest(frqStore, frpStore))

	energy := sep2.SignedRealEnergy{Value: 10000}
	frq := sep2.FlowReservationRequest{MRID: "FRQ001", EnergyRequested: &energy}
	body, err := xml.Marshal(&frq)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/edev/dev1/frq", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body: %s", w.Code, w.Body.String())
	}

	stored, err := frqStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored requests: %v", err)
	}
	if len(stored.Items) != 0 {
		t.Errorf("stored request count = %d, want 0; a mint failure must leave no orphaned request", len(stored.Items))
	}
	responses, err := frpStore.List(context.Background(), "dev1", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list stored responses: %v", err)
	}
	if len(responses.Items) != 0 {
		t.Errorf("stored response count = %d, want 0", len(responses.Items))
	}
}
