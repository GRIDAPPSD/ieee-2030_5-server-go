package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/commitment"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/handler"
)

type commitmentsFunc func(ctx context.Context, fleetKey string, now int64) (commitment.Commitments, error)

func (f commitmentsFunc) Commitments(ctx context.Context, fleetKey string, now int64) (commitment.Commitments, error) {
	return f(ctx, fleetKey, now)
}

// A failed read is a 500 naming no commitment, never a 200 with empty
// lists: an empty list says the fleet is free.
func TestAdminCommitmentsStoreErrorIs500(t *testing.T) {
	t.Parallel()
	h := &handler.AdminCommitmentsHandler{
		Ledger: commitmentsFunc(func(context.Context, string, int64) (commitment.Commitments, error) {
			return commitment.Commitments{}, errors.New("store unreachable")
		}),
		Now: func() int64 { return frNow },
	}
	w := httptest.NewRecorder()
	h.HandleList()(w, httptest.NewRequest(http.MethodGet, "/api/derms/commitments?aggregatorLFDI="+frAggLFDI, nil))
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, w.Body.String())
	}
	if w.Code != http.StatusInternalServerError || string(body["code"]) != `"internal"` {
		t.Errorf("status %d code %s, want 500 internal", w.Code, body["code"])
	}
	if _, ok := body["grants"]; ok {
		t.Errorf("a failed read served grants: %s", w.Body.String())
	}
}

// The fleet key is the aggregator LFDI, canonicalised before the ledger is
// asked, and the clock the handler is given is the one the ledger filters by.
func TestAdminCommitmentsAsksTheLedgerForTheFleet(t *testing.T) {
	t.Parallel()
	var gotFleet string
	var gotNow int64
	h := &handler.AdminCommitmentsHandler{
		Ledger: commitmentsFunc(func(_ context.Context, fleet string, now int64) (commitment.Commitments, error) {
			gotFleet, gotNow = fleet, now
			return commitment.Commitments{}, nil
		}),
		Now: func() int64 { return frNow },
	}
	w := httptest.NewRecorder()
	h.HandleList()(w, httptest.NewRequest(http.MethodGet, "/api/derms/commitments?aggregatorLFDI=3e4f45ab31edfe5b67e343e5e4562e31984e23e5", nil))
	if w.Code != http.StatusOK || gotFleet != frAggLFDI || gotNow != frNow {
		t.Fatalf("status %d, ledger asked for %q at %d; want 200, %q at %d", w.Code, gotFleet, gotNow, frAggLFDI, frNow)
	}

	for _, q := range []string{"", "?aggregatorLFDI=xyz"} {
		gotFleet = ""
		w := httptest.NewRecorder()
		h.HandleList()(w, httptest.NewRequest(http.MethodGet, "/api/derms/commitments"+q, nil))
		if w.Code != http.StatusBadRequest || gotFleet != "" {
			t.Errorf("query %q: status %d, ledger asked for %q; want 400 and no read", q, w.Code, gotFleet)
		}
	}
}
