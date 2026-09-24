package assembly_test

import (
	"context"
	"encoding/xml"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
	coresingleton "github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/handlers/singleton"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/storetest"
)

// Delegated access through a provisioned (manager, managed) LFDI pair,
// driven through the assembled router with identity from the request
// context, as in ownership_gate_test.go.
//
// The fleet: the manager owns "10" and manages "1" and "4"; another manager
// manages "2"; "3" is unmanaged.

const (
	managerID        = "10"
	managerLFDI      = "AA00000000000000000000000000000000000010"
	otherManagerLFDI = "BB00000000000000000000000000000000000020"
	unmanagedID      = "3"
	unmanagedLFDI    = "C300000000000000000000000000000000000003"
	secondChildID    = "4"
	secondChildLFDI  = "C400000000000000000000000000000000000004"
	recordlessLFDI   = "C900000000000000000000000000000000000009"
)

type managementFleet struct {
	stores   *assembly.Stores
	managers *memory.EndDeviceManagementStore
}

func newManagementFleet(t *testing.T) managementFleet {
	t.Helper()
	stores := testStores()
	seedDevice(t, stores.EndDevices, managerID, managerLFDI, "")
	seedDevice(t, stores.EndDevices, victimID, victimLFDI, victimSFDI)
	seedDevice(t, stores.EndDevices, callerID, callerLFDI, callerSFDI)
	seedDevice(t, stores.EndDevices, unmanagedID, unmanagedLFDI, "")
	seedDevice(t, stores.EndDevices, secondChildID, secondChildLFDI, "")
	seedManagedSubResources(t, stores, victimID)
	managers := memory.NewEndDeviceManagementStore()
	for _, pair := range [][2]string{
		{managerLFDI, victimLFDI},
		{managerLFDI, secondChildLFDI},
		{otherManagerLFDI, callerLFDI},
	} {
		if err := managers.Assign(context.Background(), pair[0], pair[1]); err != nil {
			t.Fatalf("assign %s -> %s: %v", pair[0], pair[1], err)
		}
	}
	stores.EndDeviceManagers = managers
	return managementFleet{stores: stores, managers: managers}
}

// seedManagedSubResources seeds one instance record under edevID for every
// GET item route the read sweep in TestManagement_ManagerReadIsServedAsTheOwnerIs
// drives: sweepStatus (via probeRequestForID) sets every wildcard in a
// pattern to the SAME id, so a DER, DERProgram, DERControl, LogEvent, or flow
// reservation instance route is probed at id "edevID" for every wildcard.
// Without a record there, the owner itself gets 404 and the sweep's
// manager-vs-owner comparison degrades to comparing two absent-resource
// answers, exactly what the 404 case in sweepStatus's callers now exists to
// catch.
func seedManagedSubResources(t *testing.T, stores *assembly.Stores, edevID string) {
	t.Helper()
	ctx := context.Background()
	seedDER(t, stores, edevID, edevID)
	if err := stores.DERPrograms.Create(ctx, edevID, edevID, sep2.DERProgram{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + edevID + "/fsa/" + edevID + "/derp/" + edevID},
		},
	}); err != nil {
		t.Fatalf("seed DERProgram: %v", err)
	}
	seedDERControl(t, stores, edevID, edevID, 0, 1700000000)
	if err := stores.LogEvents.Create(ctx, edevID, edevID, sep2.LogEvent{
		Resource: sep2.Resource{Href: "/edev/" + edevID + "/lel/" + edevID},
	}); err != nil {
		t.Fatalf("seed LogEvent: %v", err)
	}
	if err := stores.FlowReservationRequests.Create(ctx, edevID, edevID, sep2.FlowReservationRequest{
		Resource: sep2.Resource{Href: "/edev/" + edevID + "/frq/" + edevID},
	}); err != nil {
		t.Fatalf("seed FlowReservationRequest: %v", err)
	}
	if err := stores.FlowReservationResponses.Create(ctx, edevID, edevID, sep2.FlowReservationResponse{
		Event: sep2.Event{SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + edevID + "/frp/" + edevID},
		}},
	}); err != nil {
		t.Fatalf("seed FlowReservationResponse: %v", err)
	}
	// DELETE /edev/{id}/sub/{subId} needs an existing record to remove: unlike
	// the PUT routes above, a delete cannot upsert its way past a 404, so
	// without this the owner's own probe would 404 the same way the item
	// routes above did before they were seeded.
	if err := stores.Subscriptions.Create(ctx, edevID, sep2.Subscription{
		SubscribableResource: sep2.SubscribableResource{
			Resource: sep2.Resource{Href: "/edev/" + edevID + "/sub/" + edevID},
		},
		SubscribedResource: "/edev/" + edevID,
		NotificationURI:    "https://example.test/notify/" + edevID,
	}); err != nil {
		t.Fatalf("seed Subscription: %v", err)
	}
}

// assertNoFleetData checks a refusal's raw bytes for any sep2 document and any
// LFDI in the fleet.
func assertNoFleetData(t *testing.T, label string, raw []byte) {
	t.Helper()
	assertDenialLeaksNothing(t, label, raw)
	for _, lfdi := range []string{managerLFDI, otherManagerLFDI, victimLFDI, callerLFDI, unmanagedLFDI, secondChildLFDI} {
		if strings.Contains(string(raw), lfdi) {
			t.Errorf("%s: refusal carries fleet LFDI %s; body=%q", label, lfdi, raw)
		}
	}
}

func TestManagement_ManagerReadsTheManagedEndDevice(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, newManagementFleet(t).stores, gateTestPolicy())

	status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, managerLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("manager GET /edev/%s: status %d, want 200; body=%s", victimID, status, raw)
	}
	var dev sep2.EndDevice
	if err := xml.Unmarshal(raw, &dev); err != nil {
		t.Fatalf("decode: %v; body=%s", err, raw)
	}
	if dev.LFDI != victimLFDI || dev.Href != "/edev/"+victimID {
		t.Errorf("manager read LFDI %q href %q, want the managed device's %q and /edev/%s", dev.LFDI, dev.Href, victimLFDI, victimID)
	}

	if status, raw := gateRequest(t, srv, http.MethodHead, "/edev/"+victimID, managerLFDI, ""); status != http.StatusOK {
		t.Errorf("manager HEAD /edev/%s: status %d, want 200; body=%q", victimID, status, raw)
	}
}

// delegatedReadPatterns selects, from the router's own pattern list, the
// reads a manager is granted: GET on the record and every GET pattern
// strictly below it except the Registration. A manager reads what the
// managed device's own client may read.
func delegatedReadPatterns(patterns []string) []string {
	var out []string
	for _, p := range patterns {
		method, path, _ := strings.Cut(p, " ")
		if method != http.MethodGet {
			continue
		}
		if path == "/edev/{id}" || (strings.HasPrefix(path, "/edev/{id}/") && path != "/edev/{id}/rg") {
			out = append(out, p)
		}
	}
	return out
}

// writeBelowRecordPatterns selects every PUT, POST or DELETE pattern
// strictly below /edev/{id}/. It excludes PUT and DELETE on the record
// itself, which TestManagement_ManagerCannotRewriteOrDeleteTheRecordOrReadItsRegistration
// already covers.
func writeBelowRecordPatterns(patterns []string) []string {
	var out []string
	for _, p := range patterns {
		method, path, _ := strings.Cut(p, " ")
		if method == http.MethodGet || method == http.MethodHead {
			continue
		}
		if strings.HasPrefix(path, "/edev/{id}/") {
			out = append(out, p)
		}
	}
	return out
}

// managerWriteAllowlist is this test's own, independently written statement
// of the five entries a manager may use below a managed record (issue
// #510). It is not derived from the package's writeAllowlist, so a mistake
// in one does not hide behind a matching mistake in the other.
var managerWriteAllowlist = map[string]bool{
	"PUT /edev/{id}/der/{derId}/dercap": true, // DERCapability, CSIP V1.2 UTIL-002
	"PUT /edev/{id}/der/{derId}/derg":   true, // DERSettings, CSIP V1.2 UTIL-002
	"PUT /edev/{id}/der/{derId}/ders":   true, // DERStatus, CSIP V1.2 UTIL-002
	"PUT /edev/{id}/der/{derId}/dera":   true, // DERAvailability, CSIP V1.2 UTIL-002
	"POST /edev/{id}/lel":               true, // LogEvent, CSIP V1.2 UTIL-001
}

// sweepStatus drives pattern, every wildcard set explicitly to victimID (the
// managed device this fleet's pairs name), on a fresh fleet so one caller's
// write cannot change the other's answer. It names victimID directly rather
// than taking probeRequestFor's default: that default is faultProbePathValue,
// a constant from an unrelated file whose value equals victimID today only by
// coincidence, and probeRequestForID makes the coupling to victimID the
// caller states rather than one two files agree on by accident.
func sweepStatus(t *testing.T, pattern, asLFDI string) int {
	t.Helper()
	srv := gateServer(t, newManagementFleet(t).stores, gateTestPolicy())
	req, err := probeRequestForID(srv.URL, pattern, victimID)
	if err != nil {
		method, path := concreteGatePath(pattern)
		if req, err = http.NewRequest(method, srv.URL+path, nil); err != nil {
			t.Fatalf("build %s: %v", pattern, err)
		}
	}
	req.Header.Set(gateIdentityHeader, asLFDI)
	status, _ := sendGateRequest(t, req)
	return status
}

func TestManagement_ManagerReadIsServedAsTheOwnerIs(t *testing.T) {
	t.Parallel()
	_, patterns := assembly.BuildProtocolRouter(assembly.RouterConfig{}, newManagementFleet(t).stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
	delegated := delegatedReadPatterns(patterns)
	if len(delegated) == 0 {
		t.Fatal("no delegated read pattern found; the sweep would pass vacuously")
	}

	for _, p := range delegated {
		owner := sweepStatus(t, p, victimLFDI)
		// 404 guards against the same degeneration as 403 and 405: if the
		// probed id ever stopped naming a real resource, both the owner and
		// the manager would answer 404 and the comparison below would pass
		// having proven nothing about delegation.
		if owner == http.StatusForbidden || owner == http.StatusMethodNotAllowed || owner == http.StatusNotFound {
			t.Errorf("%s: the owner itself answered %d, so comparing the manager to it proves nothing", p, owner)
			continue
		}
		if manager := sweepStatus(t, p, managerLFDI); manager != owner {
			t.Errorf("%s: manager answered %d, owner %d; a delegated read serves the manager as it serves the owner", p, manager, owner)
		}
	}
	t.Logf("swept %d delegated read patterns of %d mounted", len(delegated), len(patterns))
}

// TestManagement_ManagerWriteFollowsTheAllowlistNotTheOwner is issue 510's
// property sweep (risk area 1 and 4): every write below a managed record is
// established from the router's own mounted patterns, not from the routes
// this brief happens to name, and each is checked against
// managerWriteAllowlist rather than against what the owner may do. Before
// this change every one of these patterns matched TestManagement_
// ManagerIsServedOnEveryDelegatedRouteAsTheOwnerIs (manager == owner); the
// control below is that prior behavior, reproduced by asserting the same
// equality for the five allow-listed patterns while every other write is
// refused regardless of what the owner gets.
func TestManagement_ManagerWriteFollowsTheAllowlistNotTheOwner(t *testing.T) {
	t.Parallel()
	_, patterns := assembly.BuildProtocolRouter(assembly.RouterConfig{}, newManagementFleet(t).stores, gateTestPolicy(), "serverSFDI", "serverLFDI", nil)
	writes := writeBelowRecordPatterns(patterns)
	if len(writes) == 0 {
		t.Fatal("no write-below-record pattern found; the sweep would pass vacuously")
	}

	var granted, refused int
	for _, p := range writes {
		owner := sweepStatus(t, p, victimLFDI)
		manager := sweepStatus(t, p, managerLFDI)
		if managerWriteAllowlist[p] {
			granted++
			// The degenerate-owner guard applies only here: the refused half
			// below never reads owner at all, so a degenerate owner answer
			// cannot make its assertion meaningless the way it would here.
			// 400 joins 403, 404 and 405: POST /edev/{id}/sub already answers
			// 400 for the owner (no probe body is registered for it, so the
			// probe falls back to an empty one), and if a 400-owner pattern
			// is ever allow-listed, comparing the manager to it would demand
			// the manager reproduce a probe artifact rather than a real
			// granted status. None of today's five allow-listed patterns
			// hits this.
			if owner == http.StatusForbidden || owner == http.StatusMethodNotAllowed || owner == http.StatusNotFound || owner == http.StatusBadRequest {
				t.Errorf("%s: allow-listed; the owner itself answered %d, so comparing the manager to it proves nothing", p, owner)
				continue
			}
			if manager != owner {
				t.Errorf("%s: allow-listed; manager answered %d, owner %d; want the manager served as the owner is", p, manager, owner)
			}
			continue
		}
		refused++
		if manager != http.StatusForbidden {
			t.Errorf("%s: not on the allow-list; manager answered %d, owner %d; want 403 regardless of the owner's status", p, manager, owner)
		}
	}
	if granted != len(managerWriteAllowlist) {
		t.Errorf("swept %d allow-listed write patterns, want all %d entries reachable through the mounted routes", granted, len(managerWriteAllowlist))
	}
	if refused == 0 {
		t.Error("no non-allow-listed write pattern was swept; the control that a write can still be refused never ran")
	}
	t.Logf("swept %d write-below-record patterns of %d mounted: %d allow-listed (granted), %d refused", len(writes), len(patterns), granted, refused)
}

// derSingletonParentKey is the (parentID) half of the (parentID, id) pair
// HandleSingletonGetPut reads and writes: r.PathValue("id") + "/" +
// r.PathValue("derId"). sweepStatus drives every wildcard in a pattern to
// victimID, so a DER PUT probed at victimID's record has this parent key.
func derSingletonParentKey(edevID string) string {
	return edevID + "/" + edevID
}

// TestManagement_ManagerGrantedWriteLandsOnTheManagedDevice is issue 510
// round two's MEDIUM. TestManagement_ManagerWriteFollowsTheAllowlistNotTheOwner
// proves a granted write gets the owner's STATUS, which a write silently
// rerouted to a different scope key still produces: the reroute upserts just
// as successfully as the correct target would, so the status never moves. A
// mutant that redirects every delegated write's {id} onto the manager's own
// LFDI before the handler runs, reproduced and reverted while building this
// test, left every existing assembly test green, including this file's own
// write sweep.
//
// This drives each of the five allow-listed writes as the manager, then
// reads the result back through the SAME (parentID, id) pair a later GET on
// the managed device would use: store.Get for the four DER singletons,
// store.List for the LogEvent, whose id the handler generates. A field the
// request carried, not merely the record's presence, is required, so an
// unrelated write landing on the same key by coincidence would not pass.
func TestManagement_ManagerGrantedWriteLandsOnTheManagedDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fleet := newManagementFleet(t)
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	parentKey := derSingletonParentKey(victimID)

	derCases := []struct {
		name  string
		path  string
		body  string
		lands func(t *testing.T) bool
	}{
		{
			name: "PUT dercap",
			path: "/edev/" + victimID + "/der/" + victimID + "/dercap",
			body: sep2Doc("DERCapability", `<type>91</type>`),
			lands: func(t *testing.T) bool {
				t.Helper()
				got, err := fleet.stores.DERCapabilities.Get(ctx, parentKey, coresingleton.SingletonKey)
				if err != nil {
					t.Logf("DERCapabilities.Get(%q, %q): %v", parentKey, coresingleton.SingletonKey, err)
					return false
				}
				return got.Type != nil && *got.Type == 91
			},
		},
		{
			name: "PUT derg",
			path: "/edev/" + victimID + "/der/" + victimID + "/derg",
			body: sep2Doc("DERSettings", `<updatedTime>1700000101</updatedTime>`),
			lands: func(t *testing.T) bool {
				t.Helper()
				got, err := fleet.stores.DERSettings.Get(ctx, parentKey, coresingleton.SingletonKey)
				if err != nil {
					t.Logf("DERSettings.Get(%q, %q): %v", parentKey, coresingleton.SingletonKey, err)
					return false
				}
				return got.UpdatedTime == 1700000101
			},
		},
		{
			name: "PUT ders",
			path: "/edev/" + victimID + "/der/" + victimID + "/ders",
			body: sep2Doc("DERStatus", `<readingTime>1700000102</readingTime>`),
			lands: func(t *testing.T) bool {
				t.Helper()
				got, err := fleet.stores.DERStatuses.Get(ctx, parentKey, coresingleton.SingletonKey)
				if err != nil {
					t.Logf("DERStatuses.Get(%q, %q): %v", parentKey, coresingleton.SingletonKey, err)
					return false
				}
				return got.ReadingTime == 1700000102
			},
		},
		{
			name: "PUT dera",
			path: "/edev/" + victimID + "/der/" + victimID + "/dera",
			body: sep2Doc("DERAvailability", `<readingTime>1700000103</readingTime>`),
			lands: func(t *testing.T) bool {
				t.Helper()
				got, err := fleet.stores.DERAvailabilities.Get(ctx, parentKey, coresingleton.SingletonKey)
				if err != nil {
					t.Logf("DERAvailabilities.Get(%q, %q): %v", parentKey, coresingleton.SingletonKey, err)
					return false
				}
				return got.ReadingTime == 1700000103
			},
		},
	}

	for _, tc := range derCases {
		status, raw := gateRequest(t, srv, http.MethodPut, tc.path, managerLFDI, tc.body)
		if status != http.StatusNoContent {
			t.Fatalf("%s: status %d, want 204; body=%q", tc.name, status, raw)
		}
		if !tc.lands(t) {
			t.Errorf("%s: the managed device's own store key %q does not carry the field the request sent; the write did not land where the manager addressed it", tc.name, parentKey)
		}
	}

	// LogEvent: HandlePostLogEvent mints the id, so the consumer's read path
	// is List(parentID), not a fixed Get key. seedManagedSubResources already
	// put one LogEvent under victimID, so the count moving by exactly one,
	// plus a matching LogEventID in the list, is what proves the manager's
	// POST landed there and not somewhere the pre-seeded record cannot show.
	before, err := fleet.stores.LogEvents.Count(ctx, victimID)
	if err != nil {
		t.Fatalf("LogEvents.Count before: %v", err)
	}
	lelBody := sep2Doc("LogEvent",
		`<createdDateTime>1700000200</createdDateTime><logEventCode>7</logEventCode>`+
			`<logEventID>4242</logEventID><logEventPEN>37244</logEventPEN><profileID>2</profileID>`)
	status, raw := gateRequest(t, srv, http.MethodPost, "/edev/"+victimID+"/lel", managerLFDI, lelBody)
	if status != http.StatusCreated {
		t.Fatalf("POST lel: status %d, want 201; body=%q", status, raw)
	}
	after, err := fleet.stores.LogEvents.Count(ctx, victimID)
	if err != nil {
		t.Fatalf("LogEvents.Count after: %v", err)
	}
	if after != before+1 {
		t.Errorf("POST lel: LogEvents.Count(%q) went from %d to %d, want +1; the manager's write did not add a record under the managed device's own key", victimID, before, after)
	}
	list, err := fleet.stores.LogEvents.List(ctx, victimID, store.ListOptions{Unbounded: true})
	if err != nil {
		t.Fatalf("LogEvents.List(%q): %v", victimID, err)
	}
	var found bool
	for _, ev := range list.Items {
		if ev.LogEventID == 4242 {
			found = true
		}
	}
	if !found {
		t.Errorf("POST lel: no LogEvent with LogEventID 4242 listed under the managed device's own key %q; the sent body did not land there", victimID)
	}
}

func TestManagement_ManagerCannotRewriteOrDeleteTheRecordOrReadItsRegistration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fleet := newManagementFleet(t)
	if err := fleet.stores.Registrations.Create(ctx, victimID, sep2.Registration{PIN: testFixturePIN, DateTimeRegistered: 1600000000}); err != nil {
		t.Fatalf("seed Registration: %v", err)
	}
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	before, err := fleet.stores.EndDevices.Get(ctx, victimID)
	if err != nil {
		t.Fatalf("read victim before: %v", err)
	}

	forged := `<EndDevice xmlns="urn:ieee:std:2030.5:ns"><changedTime>1</changedTime>` +
		`<lFDI>` + managerLFDI + `</lFDI><sFDI>1</sFDI></EndDevice>`
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/edev/" + victimID, forged},
		{http.MethodDelete, "/edev/" + victimID, ""},
		{http.MethodGet, "/edev/" + victimID + "/rg", ""},
	} {
		status, raw := gateRequest(t, srv, tc.method, tc.path, managerLFDI, tc.body)
		label := "manager " + tc.method + " " + tc.path
		if status != http.StatusForbidden {
			t.Errorf("%s: status %d, want 403; body=%q", label, status, raw)
		}
		assertNoFleetData(t, label, raw)
		assertNoRegistrationLeak(t, string(raw), testFixturePIN)
	}

	after, err := fleet.stores.EndDevices.Get(ctx, victimID)
	if err != nil {
		t.Fatalf("managed record is gone after a refused DELETE: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("managed record changed by refused writes:\nbefore=%+v\nafter =%+v", before, after)
	}
	if _, err := fleet.stores.Registrations.Get(ctx, victimID); err != nil {
		t.Errorf("managed device's Registration is gone after refused requests: %v", err)
	}

	// The owner reads the same Registration, so the manager's 403 is the
	// delegation boundary and not a missing record.
	if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID+"/rg", victimLFDI, ""); status != http.StatusOK {
		t.Errorf("owner GET /edev/%s/rg: status %d, want 200; body=%q", victimID, status, raw)
	}
}

func TestManagement_ManagementReachesOnlyTheManagedDevices(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fleet := newManagementFleet(t)
	srv := gateServer(t, fleet.stores, gateTestPolicy())

	derg := `<DERSettings xmlns="urn:ieee:std:2030.5:ns"><updatedTime>1</updatedTime></DERSettings>`
	for _, tc := range []struct{ name, method, path, asLFDI, body string }{
		{"device managed by another manager", http.MethodGet, "/edev/" + callerID, managerLFDI, ""},
		{"write under another manager's device", http.MethodPut, "/edev/" + callerID + "/der/x/derg", managerLFDI, derg},
		{"unmanaged device", http.MethodGet, "/edev/" + unmanagedID, managerLFDI, ""},
		{"other manager on this manager's device", http.MethodGet, "/edev/" + victimID, otherManagerLFDI, ""},
		{"managed device reaching its manager", http.MethodGet, "/edev/" + managerID, victimLFDI, ""},
		{"managed device reaching its sibling", http.MethodGet, "/edev/" + secondChildID, victimLFDI, ""},
		{"caller LFDI a prefix of the manager's", http.MethodGet, "/edev/" + victimID, managerLFDI[:20], ""},
		{"manager LFDI a prefix of the caller's", http.MethodGet, "/edev/" + victimID, managerLFDI + "00", ""},
	} {
		status, raw := gateRequest(t, srv, tc.method, tc.path, tc.asLFDI, tc.body)
		if status != http.StatusForbidden {
			t.Errorf("%s: %s %s: status %d, want 403; body=%q", tc.name, tc.method, tc.path, status, raw)
		}
		assertNoFleetData(t, tc.name, raw)
	}
	if _, err := fleet.stores.DERSettings.Get(ctx, callerID, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("refused write stored DERSettings under another manager's device: err=%v", err)
	}
}

func TestManagement_UnassignRevokesOnTheNextRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fleet := newManagementFleet(t)
	srv := gateServer(t, fleet.stores, gateTestPolicy())

	if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, managerLFDI, ""); status != http.StatusOK {
		t.Fatalf("manager GET before revocation: status %d, want 200; body=%q", status, raw)
	}
	if list := listAs(t, srv, "", managerLFDI); list.All != 3 {
		t.Fatalf("manager list before revocation: all=%d, want 3", list.All)
	}

	if err := fleet.managers.Unassign(ctx, victimLFDI); err != nil {
		t.Fatalf("Unassign: %v", err)
	}

	status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, managerLFDI, "")
	if status != http.StatusForbidden {
		t.Errorf("manager GET after revocation: status %d, want 403; body=%q", status, raw)
	}
	assertNoFleetData(t, "revoked manager", raw)
	list := listAs(t, srv, "", managerLFDI)
	if list.All != 2 || list.Results != 2 {
		t.Errorf("manager list after revocation: all=%d results=%d, want 2 and 2", list.All, list.Results)
	}
	for _, d := range list.EndDevice {
		if d.LFDI == victimLFDI {
			t.Errorf("revoked device still listed: %+v", d)
		}
	}
}

// faultyManagers fails every operation while fault is armed.
type faultyManagers struct {
	inner store.EndDeviceManagementStore
	fault *storetest.Fault
}

func (f faultyManagers) ManagerOf(ctx context.Context, managed string) (string, error) {
	if err := f.fault.Err(); err != nil {
		return "", err
	}
	return f.inner.ManagerOf(ctx, managed)
}

func (f faultyManagers) ManagedBy(ctx context.Context, manager string) ([]string, error) {
	if err := f.fault.Err(); err != nil {
		return nil, err
	}
	return f.inner.ManagedBy(ctx, manager)
}

func (f faultyManagers) Assign(ctx context.Context, manager, managed string) error {
	if err := f.fault.Err(); err != nil {
		return err
	}
	return f.inner.Assign(ctx, manager, managed)
}

func (f faultyManagers) Unassign(ctx context.Context, managed string) error {
	if err := f.fault.Err(); err != nil {
		return err
	}
	return f.inner.Unassign(ctx, managed)
}

func TestManagement_StoreFailureIs500AndRunsNoHandler(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fleet := newManagementFleet(t)
	fault := &storetest.Fault{}
	fleet.stores.EndDeviceManagers = faultyManagers{inner: fleet.managers, fault: fault}
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	fault.Arm(storetest.ErrBackendUnavailable)

	derg := `<DERSettings xmlns="urn:ieee:std:2030.5:ns"><updatedTime>1</updatedTime></DERSettings>`
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPut, "/edev/" + victimID + "/der/x/derg", derg},
		{http.MethodGet, "/edev/" + victimID, ""},
		{http.MethodGet, "/edev", ""},
	} {
		label := "manager " + tc.method + " " + tc.path + " with the management store failing"
		status, raw := gateRequest(t, srv, tc.method, tc.path, managerLFDI, tc.body)
		if status != http.StatusInternalServerError {
			t.Errorf("%s: status %d, want 500; body=%q", label, status, raw)
		}
		assertNoFleetData(t, label, raw)
		if strings.Contains(string(raw), storetest.ErrBackendUnavailable.Error()) {
			t.Errorf("%s: store error text reached the client; body=%q", label, raw)
		}
	}
	if _, err := fleet.stores.DERSettings.Get(ctx, victimID, "x"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the write handler ran behind a failed management check: err=%v", err)
	}

	// Self access never consults management.
	if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, victimLFDI, ""); status != http.StatusOK {
		t.Errorf("owner GET with the management store failing: status %d, want 200; body=%q", status, raw)
	}
	// A pattern management never grants is refused without consulting it.
	if status, raw := gateRequest(t, srv, http.MethodDelete, "/edev/"+victimID, managerLFDI, ""); status != http.StatusForbidden {
		t.Errorf("manager DELETE /edev/%s with the management store failing: status %d, want 403; body=%q", victimID, status, raw)
	}
}

func TestManagement_AbsentStoreDelegatesNothing(t *testing.T) {
	t.Parallel()
	var typedNil *memory.EndDeviceManagementStore
	for name, managers := range map[string]store.EndDeviceManagementStore{"nil": nil, "typed nil": typedNil} {
		fleet := newManagementFleet(t)
		fleet.stores.EndDeviceManagers = managers
		srv := gateServer(t, fleet.stores, gateTestPolicy())

		status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, managerLFDI, "")
		if status != http.StatusForbidden {
			t.Errorf("%s store: manager GET /edev/%s: status %d, want 403; body=%q", name, victimID, status, raw)
		}
		assertNoFleetData(t, name+" store refusal", raw)
		list := listAs(t, srv, "", managerLFDI)
		if list.All != 1 || len(list.EndDevice) != 1 || list.EndDevice[0].LFDI != managerLFDI {
			t.Errorf("%s store: manager list all=%d items=%+v, want only its own device", name, list.All, list.EndDevice)
		}
	}

	// Control: the same fleet with its store wired grants the read.
	srv := gateServer(t, newManagementFleet(t).stores, gateTestPolicy())
	if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, managerLFDI, ""); status != http.StatusOK {
		t.Errorf("control with the store wired: status %d, want 200; body=%q", status, raw)
	}
}

func TestManagement_UnprovisionedAggregatorIsAnOrdinaryDevice(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stores := testStores()
	seedDevice(t, stores.EndDevices, managerID, managerLFDI, "")
	seedDevice(t, stores.EndDevices, victimID, victimLFDI, victimSFDI)
	managers := memory.NewEndDeviceManagementStore()
	stores.EndDeviceManagers = managers
	srv := gateServer(t, stores, gateTestPolicy())

	status, raw := gateRequest(t, srv, http.MethodGet, "/edev", managerLFDI, "")
	list := decodeEndDeviceList(t, raw)
	if status != http.StatusOK || list.All != 1 || len(list.EndDevice) != 1 || list.EndDevice[0].LFDI != managerLFDI {
		t.Errorf("unprovisioned aggregator list: status %d all=%d items=%+v, want only itself", status, list.All, list.EndDevice)
	}
	if strings.Contains(string(raw), victimLFDI) {
		t.Errorf("unprovisioned aggregator list carries another device's LFDI; body=%s", raw)
	}
	status, raw = gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, managerLFDI, "")
	if status != http.StatusForbidden {
		t.Errorf("unprovisioned aggregator GET /edev/%s: status %d, want 403; body=%q", victimID, status, raw)
	}
	assertNoFleetData(t, "unprovisioned aggregator", raw)

	// Control: provisioning the pair is what grants the read.
	if err := managers.Assign(ctx, managerLFDI, victimLFDI); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	if status, raw := gateRequest(t, srv, http.MethodGet, "/edev/"+victimID, managerLFDI, ""); status != http.StatusOK {
		t.Errorf("after provisioning: status %d, want 200; body=%q", status, raw)
	}
}

func listAs(t *testing.T, srv *httptest.Server, query, asLFDI string) sep2.EndDeviceList {
	t.Helper()
	status, raw := gateRequest(t, srv, http.MethodGet, "/edev"+query, asLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("GET /edev%s as %s: status %d, want 200; body=%s", query, asLFDI, status, raw)
	}
	return decodeEndDeviceList(t, raw)
}

func TestManagement_ListHoldsSelfAndManagedDevicesInKeyOrder(t *testing.T) {
	t.Parallel()
	srv := gateServer(t, newManagementFleet(t).stores, gateTestPolicy())
	lfdiByHref := map[string]string{
		"/edev/" + victimID:      victimLFDI,
		"/edev/" + managerID:     managerLFDI,
		"/edev/" + secondChildID: secondChildLFDI,
	}

	for _, tc := range []struct {
		query     string
		wantHrefs []string
	}{
		{"", []string{"/edev/1", "/edev/10", "/edev/4"}},
		{"?s=1", []string{"/edev/10", "/edev/4"}},
		{"?l=0", nil},
		{"?a=1", []string{"/edev/10", "/edev/4"}},
		{"?s=0&l=1", []string{"/edev/1"}},
	} {
		status, raw := gateRequest(t, srv, http.MethodGet, "/edev"+tc.query, managerLFDI, "")
		if status != http.StatusOK {
			t.Errorf("GET /edev%s: status %d, want 200; body=%s", tc.query, status, raw)
			continue
		}
		list := decodeEndDeviceList(t, raw)
		var hrefs []string
		for _, d := range list.EndDevice {
			hrefs = append(hrefs, d.Href)
			if d.LFDI != lfdiByHref[d.Href] {
				t.Errorf("GET /edev%s: %s carries LFDI %q, want %q", tc.query, d.Href, d.LFDI, lfdiByHref[d.Href])
			}
		}
		if !reflect.DeepEqual(hrefs, tc.wantHrefs) {
			t.Errorf("GET /edev%s: hrefs %v, want %v", tc.query, hrefs, tc.wantHrefs)
		}
		if list.All != 3 || list.Results != uint32(len(tc.wantHrefs)) {
			t.Errorf("GET /edev%s: all=%d results=%d, want 3 and %d", tc.query, list.All, list.Results, len(tc.wantHrefs))
		}
		for _, foreign := range []string{callerLFDI, unmanagedLFDI, otherManagerLFDI} {
			if strings.Contains(string(raw), foreign) {
				t.Errorf("GET /edev%s: raw response carries %s, which the caller neither is nor manages; body=%s", tc.query, foreign, raw)
			}
		}
	}
}

// TestManagement_ListSkipsAManagedLFDIWithNoRecord cannot run in parallel: it
// swaps the process-wide log output.
func TestManagement_ListSkipsAManagedLFDIWithNoRecord(t *testing.T) {
	fleet := newManagementFleet(t)
	if err := fleet.managers.Assign(context.Background(), managerLFDI, recordlessLFDI); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	srv := gateServer(t, fleet.stores, gateTestPolicy())

	buf := &logProbeSafeBuffer{}
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetFlags(0)
	log.SetOutput(buf)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	const requests = 3
	for i := 0; i < requests; i++ {
		status, raw := gateRequest(t, srv, http.MethodGet, "/edev", managerLFDI, "")
		if status != http.StatusOK {
			t.Fatalf("GET /edev: status %d, want 200; body=%s", status, raw)
		}
		list := decodeEndDeviceList(t, raw)
		if list.All != 3 || list.Results != 3 || strings.Contains(string(raw), recordlessLFDI) {
			t.Errorf("list all=%d results=%d; want 3 and 3 with the record-less LFDI neither counted nor present; body=%s", list.All, list.Results, raw)
		}
	}
	if n := strings.Count(buf.String(), recordlessLFDI); n != 1 {
		t.Errorf("a managed LFDI with no EndDevice record was logged %d times over %d list requests, want once; log=%q", n, requests, buf.String())
	}
}

// TestManagement_ListSkipsAManagedRecordWithAMalformedHref cannot run in
// parallel: it swaps the process-wide log output.
func TestManagement_ListSkipsAManagedRecordWithAMalformedHref(t *testing.T) {
	const malformedLFDI = "C500000000000000000000000000000000000005"
	ctx := context.Background()
	fleet := newManagementFleet(t)
	dev := sep2.EndDevice{LFDI: malformedLFDI}
	dev.Href = "not-an-edev-href"
	if err := fleet.stores.EndDevices.Create(ctx, "5", dev); err != nil {
		t.Fatalf("seed malformed record: %v", err)
	}
	if err := fleet.managers.Assign(ctx, managerLFDI, malformedLFDI); err != nil {
		t.Fatalf("Assign: %v", err)
	}
	srv := gateServer(t, fleet.stores, gateTestPolicy())
	buf := captureLog(t)

	status, raw := gateRequest(t, srv, http.MethodGet, "/edev", managerLFDI, "")
	if status != http.StatusOK {
		t.Fatalf("GET /edev with one malformed managed record: status %d, want 200 listing the rest; body=%s", status, raw)
	}
	list := decodeEndDeviceList(t, raw)
	if list.All != 3 || list.Results != 3 || strings.Contains(string(raw), malformedLFDI) {
		t.Errorf("list all=%d results=%d; want 3 and 3 with the malformed record neither counted nor present; body=%s", list.All, list.Results, raw)
	}
	if !lineWith(buf.String(), "not-an-edev-href") {
		t.Errorf("the malformed record was skipped without a log line naming its href; log=%q", buf.String())
	}
}
