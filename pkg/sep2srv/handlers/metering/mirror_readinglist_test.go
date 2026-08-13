package metering_test

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

// IEEE 2030.5-2018 Annex A.4.4.11 lists TWO request representations for POST
// on /mup/{id1}: MirrorMeterReading (POST) and MirrorMeterReadingList (POST).
// The route accepted only the single form, so a batching client's Mandatory
// POST was refused. These tests cover the list form, the unchanged single
// form, and the discrimination between them.

// readingListBody builds the literal wire document a batching client sends.
// It is a string literal rather than a marshalled struct so the test asserts
// against the bytes on the wire, not against a round trip through our own
// marshaller. forgedHrefPrefix and forgedTime are the server-owned fields a
// client must not be able to set; every reading carries them.
func readingListBody(mrids []string, values []int64) []byte {
	var b strings.Builder
	b.WriteString(`<MirrorMeterReadingList xmlns="urn:ieee:std:2030.5:ns" all="`)
	fmt.Fprintf(&b, `%d" results="%d">`, len(mrids), len(mrids))
	for i, mrid := range mrids {
		fmt.Fprintf(&b,
			`<MirrorMeterReading href="%s%s"><mRID>%s</mRID><description>batch %d</description>`+
				`<lastUpdateTime>%d</lastUpdateTime><Reading><value>%d</value></Reading></MirrorMeterReading>`,
			forgedListHrefPrefix, mrid, mrid, i, forgedListTime, values[i])
	}
	b.WriteString(`</MirrorMeterReadingList>`)
	return []byte(b.String())
}

const (
	forgedListHrefPrefix = "/mup/VICTIM/mr/"
	forgedListTime       = int64(1)
)

// TestHandlePostMirrorMeterReading_ListStoresEveryReading asserts the batch is
// stored in full, with every member getting the same server-owned field
// stamping and href minting a single reading gets today. Nothing about a
// reading's treatment may depend on whether it arrived alone or in a list.
func TestHandlePostMirrorMeterReading_ListStoresEveryReading(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"
	mrids := []string{"MMR_A", "MMR_B", "MMR_C"}
	values := []int64{1100, 2200, 3300}

	for _, path := range bothPostRoutes("device-a") {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)
			mux := mirrorPostMux(mupStore, mmrStore, ownerLFDI)

			before := time.Now().Unix()
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(readingListBody(mrids, values)))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)
			after := time.Now().Unix()

			if w.Code == http.StatusBadRequest {
				t.Fatalf("POST %s with a MirrorMeterReadingList: status = 400, body = %s "+
					"(A.4.4.11 lists MirrorMeterReadingList as a POST request representation)", path, w.Body.String())
			}
			if w.Code != http.StatusCreated {
				t.Fatalf("POST %s status = %d, want 201; body = %s", path, w.Code, w.Body.String())
			}

			// Every reading landed, not just the first or the last.
			count, err := mmrStore.Count(context.Background(), "device-a")
			if err != nil {
				t.Fatalf("count readings: %v", err)
			}
			if count != uint32(len(mrids)) {
				t.Fatalf("stored reading count = %d, want %d", count, len(mrids))
			}

			listed, err := mmrStore.List(context.Background(), "device-a", store.ListOptions{Limit: 10})
			if err != nil {
				t.Fatalf("list readings: %v", err)
			}
			if len(listed.Items) != len(mrids) {
				t.Fatalf("listed reading count = %d, want %d", len(listed.Items), len(mrids))
			}

			// The store sorts by key and the minted ids are fixed-width
			// nanosecond counters assigned in document order, so listed order
			// is document order.
			hrefs := make(map[string]bool, len(listed.Items))
			for i, got := range listed.Items {
				if got.MRID != mrids[i] {
					t.Errorf("reading %d MRID = %q, want %q", i, got.MRID, mrids[i])
				}
				if got.Reading == nil || got.Reading.Value == nil || *got.Reading.Value != values[i] {
					t.Errorf("reading %d value not preserved: %+v", i, got.Reading)
				}
				if got.Description != fmt.Sprintf("batch %d", i) {
					t.Errorf("reading %d Description = %q, want %q", i, got.Description, fmt.Sprintf("batch %d", i))
				}

				// Server-owned href, minted under THIS parent, never the
				// client's claimed one.
				wantPrefix := "/mup/device-a/mr/"
				if !strings.HasPrefix(got.Href, wantPrefix) {
					t.Errorf("reading %d Href = %q, want prefix %q", i, got.Href, wantPrefix)
				}
				if strings.HasPrefix(got.Href, forgedListHrefPrefix) {
					t.Errorf("reading %d Href = %q: client-claimed href persisted verbatim", i, got.Href)
				}
				if id := strings.TrimPrefix(got.Href, wantPrefix); len(id) != 20 {
					t.Errorf("reading %d minted id %q has length %d, want 20", i, id, len(id))
				}
				if hrefs[got.Href] {
					t.Errorf("reading %d Href = %q duplicates an earlier reading's href", i, got.Href)
				}
				hrefs[got.Href] = true

				// Server-owned lastUpdateTime, never the client's.
				if got.LastUpdateTime == forgedListTime {
					t.Errorf("reading %d LastUpdateTime = %d: forged client value persisted verbatim", i, got.LastUpdateTime)
				}
				if got.LastUpdateTime < before || got.LastUpdateTime > after {
					t.Errorf("reading %d LastUpdateTime = %d, want server clock in [%d, %d]", i, got.LastUpdateTime, before, after)
				}
			}

			// Location names a resource this request actually created.
			loc := w.Header().Get("Location")
			if loc == "" {
				t.Fatal("no Location header on 201: the EPRI client strlen()s this value unguarded")
			}
			if !hrefs[loc] {
				t.Errorf("Location = %q names no reading this request created", loc)
			}
			if loc != listed.Items[0].Href {
				t.Errorf("Location = %q, want the first created reading %q", loc, listed.Items[0].Href)
			}
		})
	}
}

// TestHandlePostMirrorMeterReading_ListFromNonOwnerStoresNothing asserts a
// batch is not a path around rule (e). The ownership gate runs before the body
// is parsed, so a non-creator's list is refused whole with no member stamped,
// stored, or acknowledged.
func TestHandlePostMirrorMeterReading_ListFromNonOwnerStoresNothing(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"
	const attackerLFDI = "DEVICE_B_LFDI"

	for _, path := range bothPostRoutes("device-a") {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)
			mux := mirrorPostMux(mupStore, mmrStore, attackerLFDI)

			body := readingListBody([]string{"FORGED_A", "FORGED_B"}, []int64{7, 8})
			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusForbidden {
				t.Fatalf("POST %s as a non-creator: status = %d, want 403; body = %s", path, w.Code, w.Body.String())
			}
			assertNoBodyLeak(t, w, ownerLFDI, attackerLFDI, "DEVICE_A", "device-a")
			if loc := w.Header().Get("Location"); loc != "" {
				t.Errorf("denied batch set Location = %q, want none", loc)
			}

			// The store is unchanged: not one member of the batch was
			// partially accepted.
			count, err := mmrStore.Count(context.Background(), "device-a")
			if err != nil {
				t.Fatalf("count readings: %v", err)
			}
			if count != 0 {
				t.Errorf("stored reading count = %d, want 0: a denied batch persisted data", count)
			}

			parent, err := mupStore.Get(context.Background(), "device-a")
			if err != nil {
				t.Fatalf("get MirrorUsagePoint: %v", err)
			}
			if parent.DeviceLFDI != ownerLFDI {
				t.Errorf("parent DeviceLFDI = %q, want %q unchanged by the denied batch", parent.DeviceLFDI, ownerLFDI)
			}
			if len(parent.MirrorMeterReading) != 0 {
				t.Errorf("parent MirrorMeterReading count = %d, want 0", len(parent.MirrorMeterReading))
			}
		})
	}
}

// TestHandlePostMirrorMeterReading_SingleFormUnchanged asserts adding the list
// form did not move the single form. Exactly one reading is stored, with the
// same server-stamped href shape and Location it produced before.
func TestHandlePostMirrorMeterReading_SingleFormUnchanged(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"

	for _, path := range bothPostRoutes("device-a") {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)
			mux := mirrorPostMux(mupStore, mmrStore, ownerLFDI)

			req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(readingBody(t, "MMR_SOLO", 4242)))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusCreated {
				t.Fatalf("POST %s status = %d, want 201; body = %s", path, w.Code, w.Body.String())
			}

			count, err := mmrStore.Count(context.Background(), "device-a")
			if err != nil {
				t.Fatalf("count readings: %v", err)
			}
			if count != 1 {
				t.Fatalf("stored reading count = %d, want 1", count)
			}

			loc := w.Header().Get("Location")
			prefix := "/mup/device-a/mr/"
			if !strings.HasPrefix(loc, prefix) {
				t.Fatalf("Location = %q, want prefix %q", loc, prefix)
			}
			id := strings.TrimPrefix(loc, prefix)
			if len(id) != 20 {
				t.Errorf("minted id %q has length %d, want 20", id, len(id))
			}

			stored, err := mmrStore.Get(context.Background(), "device-a", id)
			if err != nil {
				t.Fatalf("get stored reading %q: %v", id, err)
			}
			if stored.MRID != "MMR_SOLO" {
				t.Errorf("stored MRID = %q, want MMR_SOLO", stored.MRID)
			}
			if stored.Href != loc {
				t.Errorf("stored Href = %q, want %q matching Location", stored.Href, loc)
			}
			if stored.Reading == nil || stored.Reading.Value == nil || *stored.Reading.Value != 4242 {
				t.Errorf("stored Reading value not preserved: %+v", stored.Reading)
			}
		})
	}
}

// TestHandlePostMirrorMeterReading_ListIsNotStoredAsOneEmptyReading is the
// mis-detection guard on the handler.
//
// The failure this rules out is specific and silent: decode a
// MirrorMeterReadingList into a MirrorMeterReading, get a zero value rather
// than an error, store that one empty record, and answer 201. The client sees
// success, the batch is gone, and nothing logs. So the assertion is on the
// stored COUNT and CONTENT, not on the status code: a handler exhibiting the
// bug would also return 201.
func TestHandlePostMirrorMeterReading_ListIsNotStoredAsOneEmptyReading(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"

	mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
	mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
	seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)
	mux := mirrorPostMux(mupStore, mmrStore, ownerLFDI)

	body := readingListBody([]string{"MMR_A", "MMR_B"}, []int64{10, 20})
	req := httptest.NewRequest(http.MethodPost, "/mup/device-a", bytes.NewReader(body))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", w.Code, w.Body.String())
	}

	count, err := mmrStore.Count(context.Background(), "device-a")
	if err != nil {
		t.Fatalf("count readings: %v", err)
	}
	if count == 1 {
		t.Fatalf("stored reading count = 1 for a 2-member list: the list was decoded as a single reading")
	}
	if count != 2 {
		t.Fatalf("stored reading count = %d, want 2", count)
	}

	listed, err := mmrStore.List(context.Background(), "device-a", store.ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("list readings: %v", err)
	}
	for i, got := range listed.Items {
		if got.MRID == "" {
			t.Errorf("reading %d has an empty MRID: a zero-value record was stored", i)
		}
		if got.Reading == nil || got.Reading.Value == nil {
			t.Errorf("reading %d has no Reading value: a zero-value record was stored", i)
		}
	}
}

// TestMirrorMeterReadingCrossDecodeIsAnError documents the second barrier
// behind the explicit root-element dispatch: encoding/xml refuses a crossed
// decode outright rather than yielding a zero value, because both sep2 types
// tag XMLName with a fixed element name.
//
// The handler does not rely on this, it dispatches on the document's own root
// name first. The assertion exists so that loosening or dropping either
// XMLName tag fails a test here rather than silently removing a safety net.
func TestMirrorMeterReadingCrossDecodeIsAnError(t *testing.T) {
	t.Parallel()

	listDoc := readingListBody([]string{"MMR_A"}, []int64{1})
	singleDoc := []byte(`<MirrorMeterReading xmlns="urn:ieee:std:2030.5:ns"><mRID>MMR_A</mRID></MirrorMeterReading>`)

	var asSingle sep2.MirrorMeterReading
	if err := xml.Unmarshal(listDoc, &asSingle); err == nil {
		t.Errorf("a MirrorMeterReadingList decoded into MirrorMeterReading without error, yielding %+v: "+
			"a try-one-then-the-other strategy would silently accept a batch as one empty reading", asSingle)
	}

	var asList sep2.MirrorMeterReadingList
	if err := xml.Unmarshal(singleDoc, &asList); err == nil {
		t.Errorf("a MirrorMeterReading decoded into MirrorMeterReadingList without error, yielding %d members: "+
			"a batch decoder would silently accept a single reading as an empty batch", len(asList.MirrorMeterReading))
	}
}

// TestHandlePostMirrorMeterReading_MalformedBodiesRejected covers the bodies
// that must be refused rather than stored, with nothing persisted and no
// Location minted. An empty list is included deliberately: answering 201 to it
// would mean a Location header naming nothing, and the EPRI reference client
// takes strlen of that header with no guard.
func TestHandlePostMirrorMeterReading_MalformedBodiesRejected(t *testing.T) {
	t.Parallel()
	const ownerLFDI = "DEVICE_A_LFDI"

	cases := []struct {
		name string
		body string
	}{
		{
			name: "empty list",
			body: `<MirrorMeterReadingList xmlns="urn:ieee:std:2030.5:ns" all="0" results="0"/>`,
		},
		{
			name: "wrong root element",
			body: `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns"><mRID>X</mRID></MirrorUsagePoint>`,
		},
		{
			name: "list without the sep2 name space",
			body: `<MirrorMeterReadingList><MirrorMeterReading><mRID>A</mRID></MirrorMeterReading></MirrorMeterReadingList>`,
		},
		{
			name: "single without the sep2 name space",
			body: `<MirrorMeterReading><mRID>A</mRID></MirrorMeterReading>`,
		},
		{
			name: "not XML at all",
			body: `{"mRID":"A"}`,
		},
		{
			name: "empty body",
			body: ``,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mupStore := memory.NewStore[sep2.MirrorUsagePoint]()
			mmrStore := memory.NewScopedStore[sep2.MirrorMeterReading]()
			seedOwnedMirror(t, mupStore, "device-a", "DEVICE_A", ownerLFDI)
			mux := mirrorPostMux(mupStore, mmrStore, ownerLFDI)

			req := httptest.NewRequest(http.MethodPost, "/mup/device-a", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
			}
			if loc := w.Header().Get("Location"); loc != "" {
				t.Errorf("rejected body set Location = %q, want none", loc)
			}
			count, err := mmrStore.Count(context.Background(), "device-a")
			if err != nil {
				t.Fatalf("count readings: %v", err)
			}
			if count != 0 {
				t.Errorf("stored reading count = %d, want 0 for a rejected body", count)
			}
		})
	}
}
