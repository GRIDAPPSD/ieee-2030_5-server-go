package metering_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/metering"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// PUT and DELETE on /mup/{id} are both wx:mode="M" (sep_wadl.xml:2303 and
// 2323), and neither was mounted before. These tests drive the handlers
// directly against the core routes, with no wrapper ACL in the path, which
// is the whole scope here: reachability through the bridge and server-go
// ACLs is separate work and is BLOCKED, not passing, today.
//
// Three properties here are asserted on STORE CONTENTS or on RESPONSE BYTES
// rather than on a status code, because for each of them the status code is
// green in both the correct and the broken world:
//
//   - a rejected mRID change: a 409 says the server refused, it does not say
//     the server refused WITHOUT having already written something;
//   - deviceLFDI from the certificate: a 204 is returned whether the stored
//     record ends up carrying the caller's LFDI or the one the body claimed;
//   - the DELETE cascade: a 200 is returned whether or not the readings scoped
//     under the deleted parent went with it.

const (
	putOwnerLFDI    = "AAAABBBBCCCCDDDDEEEEFFFF0000111122223333"
	putVictimLFDI   = "9999888877776666555544443333222211110000"
	putAttackerLFDI = "0123456789ABCDEF0123456789ABCDEF01234567"
)

// mupWireBody is the literal request document a client PUTs.
//
// It is assembled as text rather than marshalled from sep2.MirrorUsagePoint on
// purpose. A fixture produced by this package's own marshaller and consumed by
// this package's own unmarshaller round-trips identically whether or not the
// wire form is the one the standard declares, so such a fixture cannot fail for
// a wire-format reason. Children are in the sep.xsd sequence order (mRID,
// description, roleFlags, serviceCategoryKind, status, deviceLFDI).
//
// claimedLFDI is the field a client must never get to set. Passing a value here
// is how the tests below hand the server a forged identity claim.
func mupWireBody(mrid, description, claimedLFDI string) []byte {
	var b strings.Builder
	b.WriteString(`<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns">`)
	fmt.Fprintf(&b, `<mRID>%s</mRID>`, mrid)
	fmt.Fprintf(&b, `<description>%s</description>`, description)
	b.WriteString(`<roleFlags>09</roleFlags><serviceCategoryKind>0</serviceCategoryKind><status>1</status>`)
	if claimedLFDI != "" {
		fmt.Fprintf(&b, `<deviceLFDI>%s</deviceLFDI>`, claimedLFDI)
	}
	b.WriteString(`</MirrorUsagePoint>`)
	return []byte(b.String())
}

// mirrorInstanceMux mounts the four /mup/{id} methods core serves, against one
// caller identity. It mirrors registerMirrorRoutes so a test exercises the same
// patterns the router registers, including the method-scoped dispatch that
// produces the Allow header.
func mirrorInstanceMux(
	mupStore *memory.Store[sep2.MirrorUsagePoint],
	mmrStore *memory.ScopedStore[sep2.MirrorMeterReading],
	caller string,
) *http.ServeMux {
	provider := identityProvider(caller)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /mup/{id}", metering.HandleMirrorUsagePoint(mupStore, provider))
	mux.HandleFunc("PUT /mup/{id}", metering.HandlePutMirrorUsagePoint(mupStore, provider, nil))
	mux.HandleFunc("DELETE /mup/{id}", metering.HandleDeleteMirrorUsagePoint(mupStore, mmrStore, provider))
	mux.HandleFunc("POST /mup/{id}", metering.HandlePostMirrorMeterReading(mupStore, mmrStore, provider))
	return mux
}

// seedDerivedMirror stores a MirrorUsagePoint under the key MirrorStoreID
// actually derives for (owner, mrid), which is what a real POST /mup would have
// written. Seeding under an arbitrary id would make the rename check below
// vacuous: the stored key would not be the derived key for anything, so every
// PUT would mismatch and the test would pass for the wrong reason.
func seedDerivedMirror(t *testing.T, s *memory.Store[sep2.MirrorUsagePoint], owner, mrid, description string) string {
	t.Helper()
	id := metering.MirrorStoreID(owner, mrid)
	err := s.Create(context.Background(), id, sep2.MirrorUsagePoint{
		Resource:            sep2.Resource{Href: metering.MirrorHref(id)},
		MRID:                mrid,
		Description:         description,
		DeviceLFDI:          owner,
		RoleFlags:           sep2.RoleFlagsValue(9),
		ServiceCategoryKind: 0,
		Status:              1,
	})
	if err != nil {
		t.Fatalf("seed MirrorUsagePoint for owner %q mRID %q: %v", owner, mrid, err)
	}
	return id
}

// seedReading puts one out-of-band MirrorMeterReading under a parent id.
func seedReading(t *testing.T, s *memory.ScopedStore[sep2.MirrorMeterReading], parentID, id string, value int64) {
	t.Helper()
	v := value
	err := s.Create(context.Background(), parentID, id, sep2.MirrorMeterReading{
		Resource: sep2.Resource{Href: fmt.Sprintf("/mup/%s/mr/%s", parentID, id)},
		MRID:     "MMR_" + id,
		Reading:  &sep2.Reading{Value: &v},
	})
	if err != nil {
		t.Fatalf("seed MirrorMeterReading %q under %q: %v", id, parentID, err)
	}
}

// assertMirrorUnchanged reads the record back and fails if any field a write
// could have touched differs from what was seeded.
func assertMirrorUnchanged(t *testing.T, s *memory.Store[sep2.MirrorUsagePoint], id, wantMRID, wantOwner, wantDescription string) {
	t.Helper()
	got, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("get MirrorUsagePoint %q: %v", id, err)
	}
	if got.MRID != wantMRID {
		t.Errorf("record %q: MRID = %q, want %q unchanged", id, got.MRID, wantMRID)
	}
	if got.DeviceLFDI != wantOwner {
		t.Errorf("record %q: DeviceLFDI = %q, want %q unchanged", id, got.DeviceLFDI, wantOwner)
	}
	if got.Description != wantDescription {
		t.Errorf("record %q: Description = %q, want %q unchanged", id, got.Description, wantDescription)
	}
	if want := metering.MirrorHref(id); got.Href != want {
		t.Errorf("record %q: Href = %q, want %q unchanged", id, got.Href, want)
	}
}

// ---------------------------------------------------------------------------
// PUT: an mRID that lands on a different key is a rejection, not a rename
// ---------------------------------------------------------------------------

// TestHandlePutMirrorUsagePoint_MRIDChangeIsRejectedAndTheStoreIsUntouched is
// the data-corruption assertion, and it is deliberately not satisfied by the
// status code alone.
//
// MirrorStoreID derives the key from (owner, mRID) together, so the mRID is part
// of this resource's identity. Honouring a body that carries a different mRID as
// a rename would leave the record at {id} with a key no future request from its
// owner can derive, and would create a SECOND record at the new key: one mirror
// becomes two, one of them unaddressable, and the readings scoped under the old
// id are stranded under a parent nothing can name. A test that checked only for
// 409 would pass just as happily against a handler that wrote the new record
// first and then reported a conflict.
//
// Two owners are seeded, not one. A single-owner store cannot tell a handler
// that left the store alone from one that rewrote everything to the same value,
// and it cannot see a write that lands in another owner's record.
func TestHandlePutMirrorUsagePoint_MRIDChangeIsRejectedAndTheStoreIsUntouched(t *testing.T) {
	t.Parallel()

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")
	idB := seedDerivedMirror(t, mupStore, putVictimLFDI, "MUP_B", "original B")

	const renamedMRID = "MUP_A_RENAMED"
	renamedID := metering.MirrorStoreID(putOwnerLFDI, renamedMRID)
	if renamedID == idA {
		t.Fatal("test setup: the renamed mRID must derive a different key, or there is nothing to reject")
	}

	mux := mirrorInstanceMux(mupStore, mmrStore, putOwnerLFDI)
	req := httptest.NewRequest(http.MethodPut, "/mup/"+idA,
		bytes.NewReader(mupWireBody(renamedMRID, "renamed", "")))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("PUT /mup/%s with a different mRID: status = %d, want 409; body = %s", idA, w.Code, w.Body.String())
	}
	assertNoBodyLeak(t, w, renamedMRID, "MUP_A", putOwnerLFDI, idA)

	// THE ASSERTION THAT MATTERS: the store after the rejected call.
	//
	// Exactly two records, the two that were seeded. A rename would show three
	// (the orphan, the new record, and the other owner's) or two with the wrong
	// contents.
	count, err := mupStore.Count(context.Background())
	if err != nil {
		t.Fatalf("count MirrorUsagePoints: %v", err)
	}
	if count != 2 {
		t.Errorf("MirrorUsagePoint count = %d, want 2: the rejected PUT changed the collection", count)
	}

	// No record was created at the key the body's mRID would have derived.
	if _, err := mupStore.Get(context.Background(), renamedID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a record exists at the renamed key %q (err = %v): the rejected PUT created a second mirror", renamedID, err)
	}

	// The target record is byte-for-byte what it was: same mRID, same owner,
	// same description, same href. In particular the description from the
	// rejected body was not applied, which is what a partially-applied write
	// would look like.
	assertMirrorUnchanged(t, mupStore, idA, "MUP_A", putOwnerLFDI, "original A")

	// The other owner's record is untouched too.
	assertMirrorUnchanged(t, mupStore, idB, "MUP_B", putVictimLFDI, "original B")
}

// TestHandlePutMirrorUsagePoint_MissingMRIDIsRejectedAndTheStoreIsUntouched
// covers the neighbouring case. An absent mRID cannot be treated as "unchanged"
// by reading the stored one back: that would make an omitted required element
// silently mean whatever the server already held, and the rename check would
// then be unreachable for exactly the body that carries no identity at all.
func TestHandlePutMirrorUsagePoint_MissingMRIDIsRejectedAndTheStoreIsUntouched(t *testing.T) {
	t.Parallel()

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")

	body := []byte(`<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns">` +
		`<description>no mrid</description><roleFlags>09</roleFlags>` +
		`<serviceCategoryKind>0</serviceCategoryKind><status>1</status></MirrorUsagePoint>`)

	mux := mirrorInstanceMux(mupStore, mmrStore, putOwnerLFDI)
	req := httptest.NewRequest(http.MethodPut, "/mup/"+idA, bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT /mup/%s with no mRID: status = %d, want 400; body = %s", idA, w.Code, w.Body.String())
	}
	assertMirrorUnchanged(t, mupStore, idA, "MUP_A", putOwnerLFDI, "original A")
}

// ---------------------------------------------------------------------------
// PUT: deviceLFDI comes from the certificate, never from the body
// ---------------------------------------------------------------------------

// TestHandlePutMirrorUsagePoint_DeviceLFDIComesFromTheCertificateNotTheBody
// sends a body that claims ANOTHER device's LFDI and asserts the stored record
// still carries the caller's certificate identity.
//
// The claim is not cosmetic. authorizeMirrorOwner implements section 10.11.3
// rule (e) by comparing the caller against the stored DeviceLFDI, so a PUT that
// took DeviceLFDI from the body would let a client hand its own mirror to
// another device's identity and lock itself out, or, worse, park a record
// carrying a victim's LFDI that the victim would then be authorised to write to
// and read from. The field is the access-control key, so a client writing it is
// a client editing the ACL.
//
// The check is on the stored record AND on the served bytes of the follow-up
// GET, because those are two different failure surfaces: the store could be
// right while the response echoed the request, or the reverse.
func TestHandlePutMirrorUsagePoint_DeviceLFDIComesFromTheCertificateNotTheBody(t *testing.T) {
	t.Parallel()

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")
	idB := seedDerivedMirror(t, mupStore, putVictimLFDI, "MUP_B", "original B")

	mux := mirrorInstanceMux(mupStore, mmrStore, putOwnerLFDI)
	req := httptest.NewRequest(http.MethodPut, "/mup/"+idA,
		bytes.NewReader(mupWireBody("MUP_A", "replaced", putVictimLFDI)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT /mup/%s: status = %d, want 204; body = %s", idA, w.Code, w.Body.String())
	}
	if got, want := w.Header().Get("Location"), metering.MirrorHref(idA); got != want {
		t.Errorf("PUT Location = %q, want %q", got, want)
	}
	if w.Body.Len() != 0 {
		t.Errorf("204 carries a body %q, want none", w.Body.String())
	}

	stored, err := mupStore.Get(context.Background(), idA)
	if err != nil {
		t.Fatalf("get MirrorUsagePoint %q: %v", idA, err)
	}
	if stored.DeviceLFDI == putVictimLFDI {
		t.Fatalf("stored DeviceLFDI = %q, the value the BODY claimed: a client set its own identity", stored.DeviceLFDI)
	}
	if stored.DeviceLFDI != putOwnerLFDI {
		t.Errorf("stored DeviceLFDI = %q, want the certificate identity %q", stored.DeviceLFDI, putOwnerLFDI)
	}
	// The rest of the body WAS applied, so the test is not passing because the
	// handler ignored the whole document.
	if stored.Description != "replaced" {
		t.Errorf("stored Description = %q, want %q: the PUT did not apply the client-owned fields", stored.Description, "replaced")
	}

	// The served bytes agree with the store.
	getReq := httptest.NewRequest(http.MethodGet, "/mup/"+idA, nil)
	getW := httptest.NewRecorder()
	mux.ServeHTTP(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET /mup/%s after PUT: status = %d, want 200", idA, getW.Code)
	}
	served := getW.Body.String()
	if !strings.Contains(served, "<deviceLFDI>"+putOwnerLFDI+"</deviceLFDI>") {
		t.Errorf("served bytes do not carry the certificate LFDI as deviceLFDI; body = %s", served)
	}
	if strings.Contains(served, putVictimLFDI) {
		t.Errorf("served bytes carry the LFDI the request body claimed; body = %s", served)
	}

	// The device whose LFDI was claimed still owns its own mirror, unchanged.
	assertMirrorUnchanged(t, mupStore, idB, "MUP_B", putVictimLFDI, "original B")
}

// ---------------------------------------------------------------------------
// PUT: the rule (e) gate runs before the body is read
// ---------------------------------------------------------------------------

// TestHandlePutMirrorUsagePoint_OwnershipGateRunsBeforeTheBodyIsRead asserts
// the ORDERING, by requiring that every body an unauthorised caller can
// construct yields the IDENTICAL response.
//
// "Both are 403" is not the assertion, and would not catch the defect. If the
// body were parsed before the gate, an unauthorised caller would still end up
// refused, but the refusals would differ: malformed XML would answer 400 with a
// parser message, a body with no mRID would answer 400 with a different message,
// and a body whose mRID resolves elsewhere would answer 409. Each distinct
// answer is a bit of information about a resource the caller has no standing to
// ask about, and the parser message in particular is derived from the stored
// document's own grammar. So the comparison is over the status code, the FULL
// header set, and the body bytes, against an empty-body baseline.
//
// The bodies are chosen so that each one takes a different branch AFTER the
// gate: they are the exact inputs that would make an oracle out of a handler
// that read the body first.
func TestHandlePutMirrorUsagePoint_OwnershipGateRunsBeforeTheBodyIsRead(t *testing.T) {
	t.Parallel()

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")

	// The attacker holds a valid certificate; it simply did not create this
	// mirror. That is the case rule (e) is about.
	mux := mirrorInstanceMux(mupStore, mmrStore, putAttackerLFDI)

	do := func(body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/mup/"+idA, bytes.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}

	baseline := do(nil)
	if baseline.Code != http.StatusForbidden {
		t.Fatalf("PUT with an empty body as a non-creator: status = %d, want 403; body = %s",
			baseline.Code, baseline.Body.String())
	}
	assertNoBodyLeak(t, baseline, putOwnerLFDI, putAttackerLFDI, "MUP_A", idA)

	probes := []struct {
		name string
		body []byte
		// what a body-first handler would have answered instead
		oracle string
	}{
		{
			name:   "well-formed body carrying the victim's own mRID",
			body:   mupWireBody("MUP_A", "probe", ""),
			oracle: "409, because the mRID derives a different key under the attacker's identity",
		},
		{
			name:   "well-formed body carrying an unrelated mRID",
			body:   mupWireBody("SOMETHING_ELSE", "probe", ""),
			oracle: "409, from a different derivation",
		},
		{
			name:   "malformed XML",
			body:   []byte(`<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>`),
			oracle: "400 carrying an encoding/xml parser message",
		},
		{
			name:   "well-formed body with no mRID at all",
			body:   []byte(`<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><description>probe</description></MirrorUsagePoint>`),
			oracle: "400 with the missing-mRID message",
		},
		{
			name:   "a body claiming the attacker's own LFDI",
			body:   mupWireBody("MUP_A", "probe", putAttackerLFDI),
			oracle: "a different path through the identity stamping",
		},
	}

	for _, p := range probes {
		p := p
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			got := do(p.body)

			if got.Code != baseline.Code {
				t.Errorf("status = %d, want %d (identical to the empty-body response).\n"+
					"A differing status means the body was read before the rule (e) gate: this body would otherwise produce %s.",
					got.Code, baseline.Code, p.oracle)
			}
			if !reflect.DeepEqual(got.Header(), baseline.Header()) {
				t.Errorf("headers = %v, want %v (identical to the empty-body response)", got.Header(), baseline.Header())
			}
			if got.Body.String() != baseline.Body.String() {
				t.Errorf("body = %q, want %q (identical to the empty-body response).\n"+
					"A differing body is an oracle even when the status matches: this body would otherwise produce %s.",
					got.Body.String(), baseline.Body.String(), p.oracle)
			}
		})
	}

	// Nothing the attacker sent was applied.
	assertMirrorUnchanged(t, mupStore, idA, "MUP_A", putOwnerLFDI, "original A")
	count, err := mupStore.Count(context.Background())
	if err != nil {
		t.Fatalf("count MirrorUsagePoints: %v", err)
	}
	if count != 1 {
		t.Errorf("MirrorUsagePoint count = %d, want 1: a denied PUT created a record", count)
	}
}

// ---------------------------------------------------------------------------
// PUT: rule (a)(4) write-over
// ---------------------------------------------------------------------------

// TestHandlePutMirrorUsagePoint_MatchingMRIDIsWrittenOverWith204AndLocation
// asserts the accepted path: the new data is written over the existing record
// (rule (a)(4)), the answer is 204 with a Location header, and the write-over is
// a REPLACEMENT rather than a merge.
//
// The merge case is asserted explicitly by seeding an inline MirrorMeterReading
// and PUTting a body without one: a merge would leave the stale child served
// back on the next GET, which is data the client deliberately dropped.
//
// The out-of-band readings in the scoped store are asserted UNCHANGED in the
// same test. They are a separate collection, and rule (a)(4)'s "written over"
// language is scoped to the MirrorUsagePoint resource; silently clearing them
// on a PUT would delete metering data the request never mentioned.
func TestHandlePutMirrorUsagePoint_MatchingMRIDIsWrittenOverWith204AndLocation(t *testing.T) {
	t.Parallel()

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")

	// An inline child on the stored record, and two out-of-band readings.
	seeded, err := mupStore.Get(context.Background(), idA)
	if err != nil {
		t.Fatalf("get seeded record: %v", err)
	}
	seeded.MirrorMeterReading = []sep2.MirrorMeterReading{{MRID: "STALE_INLINE"}}
	if err := mupStore.Update(context.Background(), idA, seeded); err != nil {
		t.Fatalf("seed inline reading: %v", err)
	}
	seedReading(t, mmrStore, idA, "00000000000000000001", 11)
	seedReading(t, mmrStore, idA, "00000000000000000002", 22)

	mux := mirrorInstanceMux(mupStore, mmrStore, putOwnerLFDI)
	req := httptest.NewRequest(http.MethodPut, "/mup/"+idA,
		bytes.NewReader(mupWireBody("MUP_A", "replaced", "")))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("PUT /mup/%s: status = %d, want 204; body = %s", idA, w.Code, w.Body.String())
	}
	if got, want := w.Header().Get("Location"), metering.MirrorHref(idA); got != want {
		t.Errorf("PUT Location = %q, want %q", got, want)
	}

	stored, err := mupStore.Get(context.Background(), idA)
	if err != nil {
		t.Fatalf("get MirrorUsagePoint %q: %v", idA, err)
	}
	if stored.Description != "replaced" {
		t.Errorf("stored Description = %q, want %q", stored.Description, "replaced")
	}
	if stored.MRID != "MUP_A" {
		t.Errorf("stored MRID = %q, want %q", stored.MRID, "MUP_A")
	}
	if want := metering.MirrorHref(idA); stored.Href != want {
		t.Errorf("stored Href = %q, want %q", stored.Href, want)
	}
	if len(stored.MirrorMeterReading) != 0 {
		t.Errorf("stored MirrorMeterReading count = %d, want 0: the write-over merged instead of replacing, "+
			"so a child the PUT dropped is still served", len(stored.MirrorMeterReading))
	}

	// The out-of-band collection is a different resource and is untouched.
	count, err := mmrStore.Count(context.Background(), idA)
	if err != nil {
		t.Fatalf("count readings: %v", err)
	}
	if count != 2 {
		t.Errorf("out-of-band reading count after PUT = %d, want 2: the PUT deleted readings it never mentioned", count)
	}
}

// TestHandlePutMirrorUsagePoint_UnknownResourceIsNotFoundOnALiveRoute is the
// clean-404 case, and it fetches a PRESENT sibling through the same shape first.
//
// That control is the point. A 404 assertion on its own passes identically
// against a route that is not mounted at all, which is precisely the state this
// card is fixing, so a bare 404 test would have been green before the handler
// existed. Proving the shape serves a real resource first is what makes the
// subsequent 404 mean "no such mirror" rather than "no such route".
func TestHandlePutMirrorUsagePoint_UnknownResourceIsNotFoundOnALiveRoute(t *testing.T) {
	t.Parallel()

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")
	mux := mirrorInstanceMux(mupStore, mmrStore, putOwnerLFDI)

	// Control: the shape is live and serves a present sibling.
	live := httptest.NewRequest(http.MethodPut, "/mup/"+idA, bytes.NewReader(mupWireBody("MUP_A", "live", "")))
	liveW := httptest.NewRecorder()
	mux.ServeHTTP(liveW, live)
	if liveW.Code != http.StatusNoContent {
		t.Fatalf("control PUT of a present mirror: status = %d, want 204; body = %s", liveW.Code, liveW.Body.String())
	}

	// The same shape, an id nothing was ever stored under.
	absentID := metering.MirrorStoreID(putOwnerLFDI, "NEVER_CREATED")
	miss := httptest.NewRequest(http.MethodPut, "/mup/"+absentID, bytes.NewReader(mupWireBody("NEVER_CREATED", "x", "")))
	missW := httptest.NewRecorder()
	mux.ServeHTTP(missW, miss)
	if missW.Code != http.StatusNotFound {
		t.Fatalf("PUT of an absent mirror: status = %d, want 404; body = %s", missW.Code, missW.Body.String())
	}

	// A PUT is not a create: the absent id is still absent.
	if _, err := mupStore.Get(context.Background(), absentID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("PUT to an absent id created a record (err = %v); Update never creates", err)
	}
}

// ---------------------------------------------------------------------------
// DELETE
// ---------------------------------------------------------------------------

// TestHandleDeleteMirrorUsagePoint_CascadesReadingsAndServesTheStrippedRecord
// is the C3 scenario case: it asserts what the DELETE removed and what it
// served, neither of which the 200 says anything about.
//
// Two owners are seeded, each with readings. A cascade that dropped every
// parent's readings, or a handler that deleted the whole collection, is
// indistinguishable from a correct one when only a single parent exists.
func TestHandleDeleteMirrorUsagePoint_CascadesReadingsAndServesTheStrippedRecord(t *testing.T) {
	t.Parallel()

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "site meter A")
	idB := seedDerivedMirror(t, mupStore, putVictimLFDI, "MUP_B", "site meter B")

	// An inline child on the record that will be served back, so the rule (c)
	// stripping has something to strip. A record with no children cannot
	// distinguish a handler that strips from one that never had anything.
	withChild, err := mupStore.Get(context.Background(), idA)
	if err != nil {
		t.Fatalf("get seeded record: %v", err)
	}
	withChild.MirrorMeterReading = []sep2.MirrorMeterReading{{MRID: "INLINE_CHILD"}}
	if err := mupStore.Update(context.Background(), idA, withChild); err != nil {
		t.Fatalf("seed inline reading: %v", err)
	}

	seedReading(t, mmrStore, idA, "00000000000000000001", 11)
	seedReading(t, mmrStore, idA, "00000000000000000002", 22)
	seedReading(t, mmrStore, idB, "00000000000000000003", 33)
	seedReading(t, mmrStore, idB, "00000000000000000004", 44)

	mux := mirrorInstanceMux(mupStore, mmrStore, putOwnerLFDI)
	req := httptest.NewRequest(http.MethodDelete, "/mup/"+idA, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("DELETE /mup/%s: status = %d, want 200; body = %s", idA, w.Code, w.Body.String())
	}

	// The served BYTES, per sep_wadl.xml:2325 with rule (c). A round trip
	// through this package's own marshaller would look the same whether or not
	// the children were stripped, because the field would simply be nil either
	// way after decoding; the wire text is the only place the difference shows.
	served := w.Body.String()
	if !strings.Contains(served, "<MirrorUsagePoint") {
		t.Errorf("DELETE response is not a MirrorUsagePoint representation; body = %s", served)
	}
	if !strings.Contains(served, "<mRID>MUP_A</mRID>") {
		t.Errorf("served record is missing the deleted record's mRID; body = %s", served)
	}
	if !strings.Contains(served, "<deviceLFDI>"+putOwnerLFDI+"</deviceLFDI>") {
		t.Errorf("served record is missing deviceLFDI; body = %s", served)
	}
	if !strings.Contains(served, "<description>site meter A</description>") {
		t.Errorf("served record is not the record that was stored; body = %s", served)
	}
	if strings.Contains(served, "MirrorMeterReading") {
		t.Errorf("served record carries a MirrorMeterReading child, violating section 10.11.3 rule (c) "+
			"(a MirrorUsagePoint representation carries only first-level elements); body = %s", served)
	}

	// The parent is gone, and only that parent.
	if _, err := mupStore.Get(context.Background(), idA); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("MirrorUsagePoint %q still present after DELETE (err = %v)", idA, err)
	}
	assertMirrorUnchanged(t, mupStore, idB, "MUP_B", putVictimLFDI, "site meter B")

	// THE CASCADE. HasParent is checked BEFORE Count, and the order is kept
	// deliberately: reads no longer materialise a bucket, so a Count first
	// would no longer recreate the entry under test, but a check
	// that only holds under one ordering is one the next reader should not have
	// to work out. HasParent is also the stronger assertion of the two, since
	// Count answers zero for an absent parent and an empty one alike.
	present, err := mmrStore.HasParent(context.Background(), idA)
	if err != nil {
		t.Fatalf("HasParent %q: %v", idA, err)
	}
	if present {
		t.Errorf("the readings collection under deleted parent %q still exists: "+
			"the DELETE orphaned it, and an owner re-creating the same mRID would inherit these readings", idA)
	}
	orphans, err := mmrStore.Count(context.Background(), idA)
	if err != nil {
		t.Fatalf("count readings under %q: %v", idA, err)
	}
	if orphans != 0 {
		t.Errorf("reading count under deleted parent %q = %d, want 0", idA, orphans)
	}

	// The other owner's readings are untouched: the cascade was scoped.
	kept, err := mmrStore.Count(context.Background(), idB)
	if err != nil {
		t.Fatalf("count readings under %q: %v", idB, err)
	}
	if kept != 2 {
		t.Errorf("reading count under untouched parent %q = %d, want 2: the cascade was not scoped to one parent", idB, kept)
	}
}

// TestHandleDeleteMirrorUsagePoint_CrossDeviceDeleteDenied asserts rule (e) on
// the DELETE route. A valid certificate that did not create the mirror is
// refused, and, as with the write path, the refusal must be a refusal: the
// record and its readings are asserted still present afterwards.
func TestHandleDeleteMirrorUsagePoint_CrossDeviceDeleteDenied(t *testing.T) {
	t.Parallel()

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")
	seedReading(t, mmrStore, idA, "00000000000000000001", 11)

	mux := mirrorInstanceMux(mupStore, mmrStore, putAttackerLFDI)
	req := httptest.NewRequest(http.MethodDelete, "/mup/"+idA, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("DELETE /mup/%s as a non-creator: status = %d, want 403; body = %s", idA, w.Code, w.Body.String())
	}
	assertNoBodyLeak(t, w, putOwnerLFDI, putAttackerLFDI, "MUP_A", idA)

	assertMirrorUnchanged(t, mupStore, idA, "MUP_A", putOwnerLFDI, "original A")
	count, err := mmrStore.Count(context.Background(), idA)
	if err != nil {
		t.Fatalf("count readings: %v", err)
	}
	if count != 1 {
		t.Errorf("reading count after a denied DELETE = %d, want 1: the denial still cascaded", count)
	}
}

// TestHandleDeleteMirrorUsagePoint_UnknownResourceIsNotFoundOnALiveRoute is the
// DELETE half of the clean-404 control: a present sibling is deleted through the
// same shape first, so the 404 that follows cannot be an unmounted route.
func TestHandleDeleteMirrorUsagePoint_UnknownResourceIsNotFoundOnALiveRoute(t *testing.T) {
	t.Parallel()

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")
	mux := mirrorInstanceMux(mupStore, mmrStore, putOwnerLFDI)

	live := httptest.NewRequest(http.MethodDelete, "/mup/"+idA, nil)
	liveW := httptest.NewRecorder()
	mux.ServeHTTP(liveW, live)
	if liveW.Code != http.StatusOK {
		t.Fatalf("control DELETE of a present mirror: status = %d, want 200; body = %s", liveW.Code, liveW.Body.String())
	}

	// The same id, now absent. This is also the idempotency case: a repeated
	// DELETE must not answer 200 with an empty representation, which would tell
	// the client it had just deleted something that was not there.
	miss := httptest.NewRequest(http.MethodDelete, "/mup/"+idA, nil)
	missW := httptest.NewRecorder()
	mux.ServeHTTP(missW, miss)
	if missW.Code != http.StatusNotFound {
		t.Fatalf("repeated DELETE: status = %d, want 404; body = %s", missW.Code, missW.Body.String())
	}
}

// TestMirrorInstanceMethodsFailClosedWithoutAnIdentity asserts both new routes
// deny when no identity is present at all, rather than treating an absent
// certificate as an unscoped caller. This is the indeterminate case, and the
// only safe answer to an indeterminate authorisation check is refusal.
func TestMirrorInstanceMethodsFailClosedWithoutAnIdentity(t *testing.T) {
	t.Parallel()

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	idA := seedDerivedMirror(t, mupStore, putOwnerLFDI, "MUP_A", "original A")
	seedReading(t, mmrStore, idA, "00000000000000000001", 11)

	// identityProvider("") reports ok=false, which is the shape of a request
	// that arrived with no client certificate.
	mux := mirrorInstanceMux(mupStore, mmrStore, "")

	cases := []struct {
		method string
		body   []byte
	}{
		{http.MethodPut, mupWireBody("MUP_A", "x", "")},
		{http.MethodDelete, nil},
	}
	for _, c := range cases {
		t.Run(c.method, func(t *testing.T) {
			var r *http.Request
			if c.body == nil {
				r = httptest.NewRequest(c.method, "/mup/"+idA, nil)
			} else {
				r = httptest.NewRequest(c.method, "/mup/"+idA, bytes.NewReader(c.body))
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusForbidden {
				t.Errorf("%s with no identity: status = %d, want 403; body = %s", c.method, w.Code, w.Body.String())
			}
		})
	}

	assertMirrorUnchanged(t, mupStore, idA, "MUP_A", putOwnerLFDI, "original A")
	count, err := mmrStore.Count(context.Background(), idA)
	if err != nil {
		t.Fatalf("count readings: %v", err)
	}
	if count != 1 {
		t.Errorf("reading count after identity-less requests = %d, want 1", count)
	}
}
