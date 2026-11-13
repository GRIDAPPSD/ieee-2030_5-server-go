package adminplane

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/derwrite"
)

// A program write on any handler built from one Stores must wait on the lock
// the owner holds, because the protocol plane will share that same owner.
func TestDERWrites_OneOwnerSerializesHandlersBuiltFromOneStores(t *testing.T) {
	stores := programEditStores(t)
	owner := &derwrite.Owner{}
	stores.DERWrites = owner
	first, second := newAdminFSAHandler(stores), newAdminFSAHandler(stores)
	if first.DERWrites != owner || second.DERWrites != owner {
		t.Fatalf("handlers hold owners %p and %p, want the stores' %p", first.DERWrites, second.DERWrites, owner)
	}

	first.DERWrites.Lock()
	req := httptest.NewRequest(http.MethodPost, "/api/fsas/fsa-T/der-programs",
		strings.NewReader(`{"description":"t","primacy":2,"defaultControl":{"type":"maxLimW","maxLimW":6000}}`))
	req.SetPathValue("id", "fsa-T")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		second.HandleCreateProgramTemplate().ServeHTTP(rec, req)
	}()
	select {
	case <-done:
		first.DERWrites.Unlock()
		t.Fatalf("template create finished while the owner's lock was held: %d", rec.Code)
	case <-time.After(150 * time.Millisecond):
	}
	first.DERWrites.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("template create never finished after the lock was released")
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("template create = %d %s, want 201", rec.Code, rec.Body)
	}
}

// A Stores with no owner is a wiring bug: construction refuses it rather than
// give each handler a private lock that excludes nothing.
func TestDERWrites_NilOwnerPanicsAtConstruction(t *testing.T) {
	stores := programEditStores(t)
	stores.DERWrites = nil
	defer func() {
		if recover() == nil {
			t.Fatal("newAdminFSAHandler with a nil owner did not panic")
		}
	}()
	newAdminFSAHandler(stores)
}
