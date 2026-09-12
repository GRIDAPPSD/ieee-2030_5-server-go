package assembly

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
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
	})
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
	g := newOwnershipGate(newRecordingMux(), devs, ownerIdentity("OWNER"))

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
	g := newOwnershipGate(newRecordingMux(), devs, nil)

	status, ran := serveGated(t, g, "1")
	if status != http.StatusForbidden || ran {
		t.Errorf("nil identity: status %d, handler ran %v; want 403 and not run", status, ran)
	}

	g = newOwnershipGate(newRecordingMux(), devs, ownerIdentity("OWNER"))
	if status, ran := serveGated(t, g, "1"); status != http.StatusTeapot || !ran {
		t.Errorf("owner control: status %d, handler ran %v; want the handler to run", status, ran)
	}
}

// fullStores wires every function set so every register*Routes branch mounts.
func fullStores() *Stores {
	return &Stores{
		EndDevices:               memory.NewEndDeviceStore(),
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
	registerAll(newOwnershipGate(gatedMux, stores.EndDevices, policy.Identity), stores, policy)

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
