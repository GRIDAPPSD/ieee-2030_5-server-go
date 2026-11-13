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
	for _, tc := range []struct {
		name string
		set  bool
	}{
		{"owner set on Stores, as Run does", true},
		{"owner built by the wiring helper", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stores := programEditStores(t)
			var owner *derwrite.Owner
			if tc.set {
				owner = &derwrite.Owner{}
				stores.DERWrites = owner
			}
			first, second := newAdminFSAHandler(stores), newAdminFSAHandler(stores)
			if first.DERWrites == nil || first.DERWrites != second.DERWrites {
				t.Fatalf("handlers hold owners %p and %p, want one shared owner", first.DERWrites, second.DERWrites)
			}
			if tc.set && first.DERWrites != owner {
				t.Fatalf("handler owner = %p, want the stores' %p", first.DERWrites, owner)
			}
			if stores.DERWrites != first.DERWrites {
				t.Fatalf("stores owner = %p, want the handlers' %p", stores.DERWrites, first.DERWrites)
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
		})
	}
}
