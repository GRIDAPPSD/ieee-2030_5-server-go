package assembly

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/GRIDAPPSD/ieee-2030_5-core-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/store/memory"
)

func TestRequiresOwnership(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"GET /edev":                      false,
		"POST /edev":                     false,
		"GET /edev/{id}":                 true,
		"PUT /edev/{id}":                 true,
		"DELETE /edev/{id}/sub/{subId}":  true,
		"GET /edev/{id}/rg":              true,
		"PUT /edev":                      true,
		"DELETE /edev":                   true,
		"GET /edev/{other}/x":            true,
		"GET example.test/edev/{id}":     true,
		"GET example.test/edev":          true,
		"GET /edevices":                  false,
		"GET /dcap":                      false,
		"GET /mup/{id}":                  false,
		"GET /upt/{uptId}/mr/{mrId}/r":   false,
		"GET /rsps/{rspsId}/rsp/{rspId}": false,
	}
	for pattern, want := range cases {
		if got := requiresOwnership(pattern); got != want {
			t.Errorf("requiresOwnership(%q) = %v, want %v", pattern, got, want)
		}
	}
}

func ownerIdentity(lfdi string) func(context.Context) (string, string, bool) {
	return func(context.Context) (string, string, bool) { return lfdi, "", true }
}

// serveGated runs one request through a gate-wrapped handler and reports the
// status and whether the wrapped handler ran.
func serveGated(t *testing.T, g *ownershipGate, pathID string) (int, bool) {
	t.Helper()
	ran := false
	h := g.wrap(func(w http.ResponseWriter, _ *http.Request) {
		ran = true
		w.WriteHeader(http.StatusTeapot)
	}, false)
	req := httptest.NewRequest(http.MethodGet, "/edev/x", nil)
	if pathID != "" {
		req.SetPathValue("id", pathID)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec.Code, ran
}

func TestOwnershipGate_EmptyPathIDIsRefused(t *testing.T) {
	t.Parallel()
	devs := memory.NewEndDeviceStore()
	// A record stored under the empty key, owned by the caller: the gate must
	// refuse on the empty id without looking it up.
	if err := devs.Create(context.Background(), "", sep2.EndDevice{LFDI: "OWNER"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g := newOwnershipGate(newRecordingMux(), devs, nil, ownerIdentity("OWNER"))

	status, ran := serveGated(t, g, "")
	if status != http.StatusForbidden || ran {
		t.Errorf("empty {id}: status %d, handler ran %v; want 403 and not run", status, ran)
	}
}

func TestOwnershipGate_NilIdentityIsRefused(t *testing.T) {
	t.Parallel()
	devs := memory.NewEndDeviceStore()
	if err := devs.Create(context.Background(), "1", sep2.EndDevice{LFDI: "OWNER"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	g := newOwnershipGate(newRecordingMux(), devs, nil, nil)

	status, ran := serveGated(t, g, "1")
	if status != http.StatusForbidden || ran {
		t.Errorf("nil identity: status %d, handler ran %v; want 403 and not run", status, ran)
	}

	g = newOwnershipGate(newRecordingMux(), devs, nil, ownerIdentity("OWNER"))
	if status, ran := serveGated(t, g, "1"); status != http.StatusTeapot || !ran {
		t.Errorf("owner control: status %d, handler ran %v; want the handler to run", status, ran)
	}
}

// fullStores wires every function set so every register*Routes branch mounts.
func fullStores() *Stores {
	return &Stores{
		EndDevices:               memory.NewEndDeviceStore(),
		EndDeviceManagers:        memory.NewEndDeviceManagementStore(),
		Registrations:            memory.NewRegistrationStore(),
		MirrorUsagePoints:        memory.NewStore[sep2.MirrorUsagePoint](),
		MirrorMeterReadings:      memory.NewScopedStore[sep2.MirrorMeterReading](),
		DERs:                     memory.NewScopedStore[sep2.DER](),
		DERCapabilities:          memory.NewScopedStore[sep2.DERCapability](),
		DERSettings:              memory.NewScopedStore[sep2.DERSettings](),
		DERStatuses:              memory.NewScopedStore[sep2.DERStatus](),
		DERAvailabilities:        memory.NewScopedStore[sep2.DERAvailability](),
		DERPrograms:              memory.NewDERProgramStore(),
		DERControls:              memory.NewScopedStore[sep2.DERControl](),
		DefaultDERControls:       memory.NewScopedStore[sep2.DefaultDERControl](),
		DERCurves:                memory.NewStore[sep2.DERCurve](),
		FSAs:                     memory.NewScopedStore[sep2.FunctionSetAssignments](),
		Subscriptions:            memory.NewSubscriptionStore(),
		UsagePoints:              memory.NewStore[sep2.UsagePoint](),
		MeterReadings:            memory.NewScopedStore[sep2.MeterReading](),
		Readings:                 memory.NewScopedStore[sep2.Reading](),
		ReadingTypes:             memory.NewStore[sep2.ReadingType](),
		Configurations:           memory.NewScopedStore[sep2.Configuration](),
		DeviceStatuses:           memory.NewScopedStore[sep2.DeviceStatus](),
		LogEvents:                memory.NewScopedStore[sep2.LogEvent](),
		PowerStatuses:            memory.NewScopedStore[sep2.PowerStatus](),
		MessagingPrograms:        memory.NewStore[sep2.MessagingProgram](),
		TextMessages:             memory.NewScopedStore[sep2.TextMessage](),
		FlowReservationRequests:  memory.NewScopedStore[sep2.FlowReservationRequest](),
		FlowReservationResponses: memory.NewScopedStore[sep2.FlowReservationResponse](),
		ResponseSets:             memory.NewStore[sep2.ResponseSet](),
		Responses:                memory.NewScopedStore[sep2.Response](),
	}
}

func registerAll(mux routeRegistrar, stores *Stores, policy AuthPolicy) {
	registerEndDeviceRoutes(mux, stores, policy, nil)
	registerMirrorRoutes(mux, stores, policy, nil)
	registerDERRoutes(mux, stores)
	registerMeteringRoutes(mux, stores)
	registerNewFunctionSetRoutes(mux, stores)
}

// TestOwnershipGate_PatternListIsUnchanged registers every route twice, bare
// and through the gate, and requires the same patterns in the same order.
func TestOwnershipGate_PatternListIsUnchanged(t *testing.T) {
	t.Parallel()
	stores := fullStores()
	policy := AuthPolicy{Identity: ownerIdentity("OWNER")}

	bare := newRecordingMux()
	registerAll(bare, stores, policy)

	gatedMux := newRecordingMux()
	registerAll(newOwnershipGate(gatedMux, stores.EndDevices, stores.EndDeviceManagers, policy.Identity), stores, policy)

	if len(bare.patterns) == 0 {
		t.Fatal("no patterns registered; the comparison would pass vacuously")
	}
	if !slices.Equal(bare.patterns, gatedMux.patterns) {
		t.Errorf("gate changed the registration sequence:\nbare:  %v\ngated: %v", bare.patterns, gatedMux.patterns)
	}
	if !slices.Equal(bare.Patterns(), gatedMux.Patterns()) {
		t.Errorf("gate changed Patterns():\nbare:  %v\ngated: %v", bare.Patterns(), gatedMux.Patterns())
	}
}

func TestDelegable(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"GET /edev/{id}":                  true,
		"GET example.test/edev/{id}":      true,
		"GET /edev/{id}/der":              true,
		"PUT /edev/{id}/der/{derId}/derg": true,
		"POST /edev/{id}/sub":             true,
		"DELETE /edev/{id}/sub/{subId}":   true,
		"DELETE /edev/{id}/lel/{lelId}":   true,
		"PUT /edev/{id}":                  false,
		"DELETE /edev/{id}":               false,
		"GET /edev/{id}/rg":               false,
		"GET /edev/{id}/{name}":           false,
		"GET /edev/{id}/":                 false,
		"GET /edevx/{id}/der":             false,
		"GET /edev":                       false,
		"POST /edev":                      false,
		"/edev/{id}":                      false,
		"GET /edev/{other}/der":           false,
		"GET /mup/{id}":                   false,
	}
	for pattern, want := range cases {
		if got := delegable(pattern); got != want {
			t.Errorf("delegable(%q) = %v, want %v", pattern, got, want)
		}
	}
}

// TestFullStoresWiresEveryStoresField fails when a Stores field is added and
// fullStores neither sets it nor states why it is left unset.
func TestFullStoresWiresEveryStoresField(t *testing.T) {
	t.Parallel()
	unset := map[string]string{
		"EndDeviceIndexes":   "nil selects the process-local index",
		"AdminFSAs":          "the admin plane is not mounted on the protocol router",
		"RegistrationPolicy": "the pattern-list comparison provisions no Registration",
	}
	v := reflect.ValueOf(fullStores()).Elem()
	seen := map[string]bool{}
	for i := 0; i < v.NumField(); i++ {
		name := v.Type().Field(i).Name
		seen[name] = true
		reason, allowed := unset[name]
		switch zero := v.Field(i).IsZero(); {
		case zero && !allowed:
			t.Errorf("fullStores leaves Stores.%s unset; set it or state why not", name)
		case !zero && allowed:
			t.Errorf("fullStores sets Stores.%s, which is listed as unset (%s)", name, reason)
		}
	}
	for name := range unset {
		if !seen[name] {
			t.Errorf("the unset list names Stores.%s, which does not exist", name)
		}
	}
}

func TestDenialLog_BoundsVolumeAndReportsWhatItSuppressed(t *testing.T) {
	t.Parallel()
	clock := newFakeDenialClock()
	d, out := newTestDenialLog(clock)
	req := httptest.NewRequest(http.MethodGet, "/edev/1", nil)
	req.SetPathValue("id", "1")
	v := ownershipVerdict{caller: "CALLER", reason: reasonNotOwner}

	const probes = 1000
	for i := 0; i < probes; i++ {
		d.record(req, v)
	}
	if lines := out.snapshot(); len(lines) != denialLogPerCaller {
		t.Fatalf("%d denials from one caller in one window wrote %d lines, want %d", probes, len(lines), denialLogPerCaller)
	}

	clock.Advance(denialLogWindow)
	want := fmt.Sprintf("suppressed %d denial log lines in the last 1m0s: not-owner=%d", probes-denialLogPerCaller, probes-denialLogPerCaller)
	if lines := out.snapshot(); len(lines) != denialLogPerCaller+1 || !strings.HasSuffix(lines[denialLogPerCaller], want) {
		t.Fatalf("after the window closed: lines %q, want a line ending %q", lines[denialLogPerCaller:], want)
	}

	out.reset()
	req.SetPathValue("id", "x\nforged "+strings.Repeat("A", 200))
	d.record(req, v)
	if lines := out.snapshot(); len(lines) != 1 || strings.Contains(lines[0], "\n") || strings.Contains(lines[0], strings.Repeat("A", maxLoggedIDLen)) {
		t.Errorf("a hostile id was not quoted and truncated: %q", lines)
	}
}
