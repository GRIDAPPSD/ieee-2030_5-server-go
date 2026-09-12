package assembly_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// The router, over a SECOND implementation of the store contract.
//
// This file is what makes the store interfaces a seam rather than a
// declaration. The compile-time assertions in pkg/store/memory prove an
// implementation has the right method set; nothing there, and nothing in the
// rest of this package's tests, proves the ROUTER can be served by an
// implementation that is not pkg/store/memory. Every assertion below passes
// only if the handlers reach their state through the interface method set, and
// fails the moment one of them reaches for something only the memory store
// provides.
//
// The second implementation is [storetest.Fake] and [storetest.ScopedFake],
// which already exist as the independent implementation the contract's own
// conformance suite runs against. Writing a third one here would be a second
// copy of the same idea, and a worse one: those two are already checked against
// the contract method by method, so wiring THEM through BuildProtocolRouter
// extends a verified claim rather than starting a fresh unverified one. They
// share no code and no data structure with the memory store: a flat slice
// sorted at list time rather than a map plus a maintained sort order, and for
// the scoped half no per-parent object at all. The memory store used to differ
// here, allocating a bucket for an unknown parent on read; that behavior was
// removed, so the two implementations now agree that a read creates nothing.
//
// The requests cover both halves of the contract in both directions: a flat
// POST and GET (UsagePoint, [store.ResourceStore]) and a scoped POST, GET and
// list (LogEvent, [store.ScopedStore]).

// fakeEndDevices is [storetest.Fake] plus the two identity lookups that
// [store.EndDeviceStore] adds.
//
// It carries NO secondary index, where the memory implementation maintains an
// sfdi-to-key and an lfdi-to-key map: both lookups here are an unbounded List
// followed by a scan. That is a poor way to run a fleet and a good way to show
// the identity lookups are a contract a consumer can satisfy however it likes,
// rather than a description of the memory store's two maps.
type fakeEndDevices struct {
	*storetest.Fake[sep2.EndDevice]
}

func newFakeEndDevices() *fakeEndDevices {
	return &fakeEndDevices{Fake: storetest.NewFake[sep2.EndDevice]()}
}

func (s *fakeEndDevices) GetBySFDI(ctx context.Context, sfdi string) (sep2.EndDevice, error) {
	return s.findBy(ctx, sfdi, func(d sep2.EndDevice) string { return d.SFDI })
}

func (s *fakeEndDevices) GetByLFDI(ctx context.Context, lfdi string) (sep2.EndDevice, error) {
	return s.findBy(ctx, lfdi, func(d sep2.EndDevice) string { return d.LFDI })
}

// findBy returns the device whose identity field equals want.
//
// An empty want never matches, mirroring the memory implementation, which
// indexes a device only under a non-empty SFDI or LFDI. Without that guard a
// lookup for "" would return whichever unidentified device happened to be
// stored first, which is an identity answer invented out of an absent one.
//
// A List failure is returned rather than flattened into ErrNotFound: "the
// backend did not answer" and "no such device" are different facts, and on the
// EndDevice create path the second one is the branch that provisions a new
// device.
func (s *fakeEndDevices) findBy(ctx context.Context, want string, field func(sep2.EndDevice) string) (sep2.EndDevice, error) {
	if want == "" {
		return sep2.EndDevice{}, store.ErrNotFound
	}
	res, err := s.List(ctx, store.ListOptions{Unbounded: true})
	if err != nil {
		return sep2.EndDevice{}, err
	}
	for _, d := range res.Items {
		if field(d) == want {
			return d, nil
		}
	}
	return sep2.EndDevice{}, store.ErrNotFound
}

// fakeStores builds a Stores whose every store field is the second
// implementation.
//
// Three fields are NOT converted and cannot be, because pkg/store models
// nothing they do:
//
//   - EndDeviceIndexes is an index allocator, not a collection.
//   - Subscriptions is queried by resource href and by device, and its records
//     carry a store key alongside the resource; no interface here expresses it.
//   - AdminFSAs is an operator-facing management plane with attach, assign and
//     reverse-lookup operations, and a List that takes no paging at all.
//
// They stay concrete here, which is what they are on Stores.
func fakeStores() *assembly.Stores {
	return &assembly.Stores{
		EndDevices:          newFakeEndDevices(),
		Registrations:       storetest.NewFake[sep2.Registration](),
		RegistrationPolicy:  testRegistrationPolicy(),
		MirrorUsagePoints:   storetest.NewFake[sep2.MirrorUsagePoint](),
		MirrorMeterReadings: storetest.NewScopedFake[sep2.MirrorMeterReading](),

		DERs:               storetest.NewScopedFake[sep2.DER](),
		DERCapabilities:    storetest.NewScopedFake[sep2.DERCapability](),
		DERSettings:        storetest.NewScopedFake[sep2.DERSettings](),
		DERStatuses:        storetest.NewScopedFake[sep2.DERStatus](),
		DERAvailabilities:  storetest.NewScopedFake[sep2.DERAvailability](),
		DERPrograms:        storetest.NewScopedFake[sep2.DERProgram](),
		DERControls:        storetest.NewScopedFake[sep2.DERControl](),
		DefaultDERControls: storetest.NewScopedFake[sep2.DefaultDERControl](),
		DERCurves:          storetest.NewFake[sep2.DERCurve](),

		FSAs:          storetest.NewScopedFake[sep2.FunctionSetAssignments](),
		Subscriptions: memory.NewSubscriptionStore(),

		UsagePoints:   storetest.NewFake[sep2.UsagePoint](),
		MeterReadings: storetest.NewScopedFake[sep2.MeterReading](),
		Readings:      storetest.NewScopedFake[sep2.Reading](),
		ReadingTypes:  storetest.NewFake[sep2.ReadingType](),

		Configurations:           storetest.NewScopedFake[sep2.Configuration](),
		DeviceStatuses:           storetest.NewScopedFake[sep2.DeviceStatus](),
		LogEvents:                storetest.NewScopedFake[sep2.LogEvent](),
		PowerStatuses:            storetest.NewScopedFake[sep2.PowerStatus](),
		MessagingPrograms:        storetest.NewFake[sep2.MessagingProgram](),
		TextMessages:             storetest.NewScopedFake[sep2.TextMessage](),
		FlowReservationRequests:  storetest.NewScopedFake[sep2.FlowReservationRequest](),
		FlowReservationResponses: storetest.NewScopedFake[sep2.FlowReservationResponse](),
		ResponseSets:             storetest.NewFake[sep2.ResponseSet](),
		Responses:                storetest.NewScopedFake[sep2.Response](),
	}
}

// fakeBackedServer boots the real router over the second implementation.
func fakeBackedServer(t *testing.T, stores *assembly.Stores) *httptest.Server {
	t.Helper()
	seedOwnedDevices(t, stores.EndDevices, "3", "7", "999")
	handler, _ := assembly.BuildProtocolRouter(
		assembly.RouterConfig{}, stores, testAuthPolicy(), "serverSFDI", "serverLFDI", nil,
	)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

// TestSecondImplementation_FlatStoreServesPostAndGet drives POST /upt and
// GET /upt/{id} against a [store.ResourceStore] that is not the memory store.
//
// The assertions are on FIELD VALUES of the resource that came back, not on the
// status code alone: a 200 carrying a zero-valued UsagePoint would satisfy a
// status-only check while proving the write never reached the store, and the
// href is the field a client follows next.
func TestSecondImplementation_FlatStoreServesPostAndGet(t *testing.T) {
	t.Parallel()

	stores := fakeStores()
	srv := fakeBackedServer(t, stores)

	const body = `<UsagePoint xmlns="urn:ieee:std:2030.5:ns">` +
		`<mRID>0A1B2C3D4E5F60718293A4B5C6D7E8F9</mRID>` +
		`<serviceCategoryKind>0</serviceCategoryKind>` +
		`<status>1</status>` +
		`</UsagePoint>`

	resp, err := http.Post(srv.URL+"/upt", "application/sep+xml", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /upt: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /upt status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("POST /upt returned no Location header, so there is no resource to follow")
	}

	// Follow the server's own Location header, which is the only address a
	// client has, and check the document served is the one that was posted.
	got, err := http.Get(srv.URL + location)
	if err != nil {
		t.Fatalf("GET %s: %v", location, err)
	}
	if got.StatusCode != http.StatusOK {
		_ = got.Body.Close()
		t.Fatalf("GET %s status = %d, want 200", location, got.StatusCode)
	}
	var upt sep2.UsagePoint
	decodeXML(t, got, &upt)

	if upt.MRID != "0A1B2C3D4E5F60718293A4B5C6D7E8F9" {
		t.Errorf("mRID = %q, want the posted value", upt.MRID)
	}
	if upt.Status != 1 {
		t.Errorf("status = %d, want 1", upt.Status)
	}
	if upt.Href != location {
		t.Errorf("href = %q, want the Location the server minted, %q", upt.Href, location)
	}

	// The list route reads the same collection through the same interface.
	listResp, err := http.Get(srv.URL + "/upt")
	if err != nil {
		t.Fatalf("GET /upt: %v", err)
	}
	var list sep2.UsagePointList
	decodeXML(t, listResp, &list)
	if list.All != 1 || list.Results != 1 || len(list.UsagePoint) != 1 {
		t.Fatalf("GET /upt: all=%d results=%d items=%d, want 1/1/1",
			list.All, list.Results, len(list.UsagePoint))
	}
	if list.UsagePoint[0].MRID != upt.MRID {
		t.Errorf("listed mRID = %q, want %q", list.UsagePoint[0].MRID, upt.MRID)
	}

	// And the resource really is in THIS store, read directly through the
	// contract rather than off the wire, so a handler holding state of its own
	// would fail here rather than pass.
	id := strings.TrimPrefix(location, "/upt/")
	stored, err := stores.UsagePoints.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("the second implementation does not hold /upt/%s: %v", id, err)
	}
	if stored.MRID != upt.MRID {
		t.Errorf("stored mRID = %q, want %q", stored.MRID, upt.MRID)
	}
}

// TestSecondImplementation_ScopedStoreServesPostAndGet drives
// POST /edev/{id}/lel, the instance GET and the list GET against a
// [store.ScopedStore] that is not the memory store.
//
// The scoped half is where the two implementations differ most: memory holds a
// map of per-parent stores and materializes a parent on read, while the fake
// holds one flat slice and materializes nothing. A handler that depended on the
// parent bucket existing would fail here.
func TestSecondImplementation_ScopedStoreServesPostAndGet(t *testing.T) {
	t.Parallel()

	stores := fakeStores()
	srv := fakeBackedServer(t, stores)

	const deviceID = "7"
	const body = `<LogEvent xmlns="urn:ieee:std:2030.5:ns">` +
		`<createdDateTime>1700000000</createdDateTime>` +
		`<logEventCode>1</logEventCode>` +
		`<logEventID>42</logEventID>` +
		`<logEventPEN>37244</logEventPEN>` +
		`<profileID>2</profileID>` +
		`</LogEvent>`

	resp, err := http.Post(srv.URL+"/edev/"+deviceID+"/lel", "application/sep+xml", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /edev/%s/lel: %v", deviceID, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /edev/%s/lel status = %d, want %d", deviceID, resp.StatusCode, http.StatusCreated)
	}
	location := resp.Header.Get("Location")
	if !strings.HasPrefix(location, "/edev/"+deviceID+"/lel/") {
		t.Fatalf("Location = %q, want an instance under /edev/%s/lel/", location, deviceID)
	}

	got, err := http.Get(srv.URL + location)
	if err != nil {
		t.Fatalf("GET %s: %v", location, err)
	}
	if got.StatusCode != http.StatusOK {
		_ = got.Body.Close()
		t.Fatalf("GET %s status = %d, want 200", location, got.StatusCode)
	}
	var event sep2.LogEvent
	decodeXML(t, got, &event)

	if event.LogEventID != 42 {
		t.Errorf("logEventID = %d, want 42", event.LogEventID)
	}
	if event.LogEventPEN != 37244 {
		t.Errorf("logEventPEN = %d, want 37244", event.LogEventPEN)
	}
	if event.Href != location {
		t.Errorf("href = %q, want the Location the server minted, %q", event.Href, location)
	}

	listResp, err := http.Get(srv.URL + "/edev/" + deviceID + "/lel")
	if err != nil {
		t.Fatalf("GET /edev/%s/lel: %v", deviceID, err)
	}
	var list sep2.LogEventList
	decodeXML(t, listResp, &list)
	if list.All != 1 || len(list.LogEvent) != 1 {
		t.Fatalf("GET /edev/%s/lel: all=%d items=%d, want 1/1", deviceID, list.All, len(list.LogEvent))
	}
	if list.LogEvent[0].LogEventID != 42 {
		t.Errorf("listed logEventID = %d, want 42", list.LogEvent[0].LogEventID)
	}

	// Scoping is real rather than incidental: the same list under another
	// device is empty, and reading that unknown parent must not bring it into
	// existence.
	otherResp, err := http.Get(srv.URL + "/edev/999/lel")
	if err != nil {
		t.Fatalf("GET /edev/999/lel: %v", err)
	}
	var otherList sep2.LogEventList
	decodeXML(t, otherResp, &otherList)
	if otherList.All != 0 || len(otherList.LogEvent) != 0 {
		t.Errorf("GET /edev/999/lel: all=%d items=%d, want an empty list", otherList.All, len(otherList.LogEvent))
	}
	if has, err := stores.LogEvents.HasParent(context.Background(), "999"); err != nil {
		t.Fatalf("HasParent(999): %v", err)
	} else if has {
		t.Error("reading the list of an unknown parent materialized it, which the contract does not ask for")
	}

	parents, err := stores.LogEvents.Parents(context.Background())
	if err != nil {
		t.Fatalf("Parents: %v", err)
	}
	if len(parents) != 1 || parents[0] != deviceID {
		t.Errorf("Parents = %v, want exactly [%q]", parents, deviceID)
	}
}

// TestSecondImplementation_SingletonPutThenGet drives PUT and GET on a DER
// sub-resource, the scoped path that upserts: the first PUT creates, and the
// second reaches the update branch, which used to run through a per-parent
// handle that only the memory store could hand out.
func TestSecondImplementation_SingletonPutThenGet(t *testing.T) {
	t.Parallel()

	stores := fakeStores()
	srv := fakeBackedServer(t, stores)

	const path = "/edev/3/der/1/ders"
	put := func(t *testing.T, opState int) {
		t.Helper()
		body := `<DERStatus xmlns="urn:ieee:std:2030.5:ns">` +
			`<readingTime>1700000000</readingTime>` +
			`<operationalModeStatus><dateTime>1700000000</dateTime>` +
			`<value>` + string(rune('0'+opState)) + `</value></operationalModeStatus>` +
			`</DERStatus>`
		req, err := http.NewRequest(http.MethodPut, srv.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatalf("build PUT: %v", err)
		}
		req.Header.Set("Content-Type", "application/sep+xml")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("PUT %s status = %d, want 204", path, resp.StatusCode)
		}
	}

	put(t, 1)
	put(t, 2) // the second write takes the update branch

	got, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	var status sep2.DERStatus
	decodeXML(t, got, &status)
	if status.OperationalModeStatus == nil {
		t.Fatal("operationalModeStatus is absent, so neither PUT reached the store")
	}
	if status.OperationalModeStatus.Value != 2 {
		t.Errorf("operationalModeStatus/value = %d, want 2 from the second PUT",
			status.OperationalModeStatus.Value)
	}
}

// TestSecondImplementation_MirrorDeleteFailsClosedWithoutCascade pins the
// fail-closed branch that a scoped store without the cascade capability reaches
// on DELETE /mup/{id}.
//
// The children of a MirrorUsagePoint are addressed only through the parent id,
// so deleting the parent without them strands them where nothing can reach
// them, and a mirror re-created under the same mRID would inherit them. The
// metering handler asserts for the cascade capability at the point of use;
// [storetest.ScopedFake] does not have it, so the delete must be REFUSED rather
// than completed. That branch is unreachable from the memory store, which does
// have the capability, which makes this exactly the case a second
// implementation exists to reach.
//
// The response status is the smaller half of the assertion. What matters is
// that the parent is still there afterwards: a 500 with the record gone would
// be the orphaning this branch exists to prevent, reported after the fact.
func TestSecondImplementation_MirrorDeleteFailsClosedWithoutCascade(t *testing.T) {
	t.Parallel()

	stores := fakeStores()
	srv := fakeBackedServer(t, stores)

	const body = `<MirrorUsagePoint xmlns="urn:ieee:std:2030.5:ns">` +
		`<mRID>1A2B3C4D5E6F70819293A4B5C6D7E8F9</mRID>` +
		`<description>second implementation</description>` +
		`<deviceLFDI>` + testLFDI + `</deviceLFDI>` +
		`<serviceCategoryKind>0</serviceCategoryKind>` +
		`<status>1</status>` +
		`</MirrorUsagePoint>`

	resp, err := http.Post(srv.URL+"/mup", "application/sep+xml", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /mup: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /mup status = %d, want 201", resp.StatusCode)
	}
	location := resp.Header.Get("Location")
	if location == "" {
		t.Fatal("POST /mup returned no Location header")
	}

	req, err := http.NewRequest(http.MethodDelete, srv.URL+location, nil)
	if err != nil {
		t.Fatalf("build DELETE: %v", err)
	}
	del, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", location, err)
	}
	_ = del.Body.Close()
	if del.StatusCode != http.StatusInternalServerError {
		t.Fatalf("DELETE %s status = %d, want 500: a store that cannot cascade must not complete the delete",
			location, del.StatusCode)
	}

	id := strings.TrimPrefix(location, "/mup/")
	if _, err := stores.MirrorUsagePoints.Get(context.Background(), id); err != nil {
		t.Fatalf("the MirrorUsagePoint was removed by a delete that failed: %v", err)
	}
	still, err := http.Get(srv.URL + location)
	if err != nil {
		t.Fatalf("GET %s: %v", location, err)
	}
	_ = still.Body.Close()
	if still.StatusCode != http.StatusOK {
		t.Errorf("GET %s after the refused delete = %d, want 200", location, still.StatusCode)
	}
}

// Compile-time proof that the second implementation is complete, measured
// against the CONTRACT rather than against whatever the tests above happen to
// call. Without these a missing method surfaces as an error at one call site
// and reads as a problem with that call site.
var (
	_ store.ResourceStore[sep2.UsagePoint] = (*storetest.Fake[sep2.UsagePoint])(nil)
	_ store.ScopedStore[sep2.LogEvent]     = (*storetest.ScopedFake[sep2.LogEvent])(nil)
	_ store.EndDeviceStore                 = (*fakeEndDevices)(nil)
)
