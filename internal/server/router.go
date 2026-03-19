package server

import (
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
	"github.com/craig8/ieee-2030_5-go/internal/paging"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

// Stores holds all resource stores for the server.
type Stores struct {
	EndDevices          store.EndDeviceStore
	MirrorUsagePoints   *memory.Store[sep2.MirrorUsagePoint]
	MirrorMeterReadings *memory.ScopedStore[sep2.MirrorMeterReading]

	// DER stores
	DERs               *memory.ScopedStore[sep2.DER]
	DERCapabilities    *memory.ScopedStore[sep2.DERCapability]
	DERSettings        *memory.ScopedStore[sep2.DERSettings]
	DERStatuses        *memory.ScopedStore[sep2.DERStatus]
	DERAvailabilities  *memory.ScopedStore[sep2.DERAvailability]
	DERPrograms        *memory.ScopedStore[sep2.DERProgram]
	DERControls        *memory.ScopedStore[sep2.DERControl]
	DefaultDERControls *memory.ScopedStore[sep2.DefaultDERControl]
	DERCurves          *memory.Store[sep2.DERCurve]

	// FSA store
	FSAs *memory.ScopedStore[sep2.FunctionSetAssignments]

	// Subscription store
	Subscriptions *memory.SubscriptionStore
}

// NewRouter creates the HTTP router for the protocol listener.
func NewRouter(cfg *config.Config, stores *Stores, svc *handler.AdminCertService, serverSFDI, serverLFDI string) http.Handler {
	top := http.NewServeMux()

	protocolMux := http.NewServeMux()
	protocolMux.HandleFunc("GET /dcap", handler.HandleDeviceCapability())
	protocolMux.HandleFunc("GET /tm", handler.HandleTime(cfg))
	protocolMux.HandleFunc("GET /sdev", handler.HandleSelfDevice(serverSFDI, serverLFDI))

	if stores != nil {
		registerEndDeviceRoutes(protocolMux, stores)
		registerMirrorRoutes(protocolMux, stores)
		registerDERRoutes(protocolMux, stores)
	}

	aclRules := auth.DefaultACLRules()
	protocolChain := auth.IdentityMiddleware(auth.ACLMiddleware(aclRules)(protocolMux))

	for _, prefix := range []string{"/dcap", "/tm", "/sdev", "/edev", "/edev/", "/mup", "/mup/", "/dc", "/dc/"} {
		top.Handle(prefix, protocolChain)
	}

	if svc != nil {
		adminMux := http.NewServeMux()
		adminMux.HandleFunc("GET /api/certs/ca", svc.HandleGetCA())
		adminMux.HandleFunc("POST /api/certs/server", svc.HandleCreateServerCert())
		adminMux.HandleFunc("POST /api/certs/device", svc.HandleCreateDeviceCert())
		top.Handle("/api/", auth.AdminAuthMiddleware(cfg.AdminKey)(adminMux))
	}

	return top
}

func registerEndDeviceRoutes(mux *http.ServeMux, stores *Stores) {
	mux.HandleFunc("GET /edev", handler.ListHandler[sep2.EndDevice, sep2.EndDeviceList](
		stores.EndDevices, handler.BuildEndDeviceList, 900,
	))
	mux.HandleFunc("POST /edev", handler.HandleCreateEndDevice(stores.EndDevices))
	mux.HandleFunc("GET /edev/{id}", handler.HandleEndDevice(stores.EndDevices))
	mux.HandleFunc("PUT /edev/{id}", handler.HandleUpdateEndDevice(stores.EndDevices))
	mux.HandleFunc("DELETE /edev/{id}", handler.HandleDeleteEndDevice(stores.EndDevices))

	// FSA endpoints
	if stores.FSAs != nil {
		mux.HandleFunc("GET /edev/{id}/fsa", scopedListHandler[sep2.FunctionSetAssignments, sep2.FunctionSetAssignmentsList](
			stores.FSAs, handler.BuildFSAList, 900,
		))
		mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}", handler.HandleFSA(stores.FSAs))
	}

	// Subscription endpoints
	if stores.Subscriptions != nil {
		mux.HandleFunc("GET /edev/{id}/sub", handler.ListHandler[sep2.Subscription, sep2.SubscriptionList](
			stores.Subscriptions.Store, handler.BuildSubscriptionList, 900,
		))
		mux.HandleFunc("POST /edev/{id}/sub", handler.HandleCreateSubscription(stores.Subscriptions))
		mux.HandleFunc("DELETE /edev/{id}/sub/{subId}", handler.HandleDeleteSubscription(stores.Subscriptions))
	}
}

func registerMirrorRoutes(mux *http.ServeMux, stores *Stores) {
	if stores.MirrorUsagePoints == nil {
		return
	}
	mux.HandleFunc("GET /mup", handler.ListHandler[sep2.MirrorUsagePoint, sep2.MirrorUsagePointList](
		stores.MirrorUsagePoints, handler.BuildMirrorUsagePointList, 300,
	))
	mux.HandleFunc("POST /mup", handler.HandleCreateMirrorUsagePoint(stores.MirrorUsagePoints))
	mux.HandleFunc("GET /mup/{id}", handler.HandleMirrorUsagePoint(stores.MirrorUsagePoints))
	mux.HandleFunc("POST /mup/{id}/mr", handler.HandlePostMirrorMeterReading(
		stores.MirrorUsagePoints, stores.MirrorMeterReadings,
	))
}

func registerDERRoutes(mux *http.ServeMux, stores *Stores) {
	if stores.DERs == nil {
		return
	}

	// DER list and singleton sub-resources under edev
	dercap, derg, ders, dera := handler.DERSingletonHandlers(
		stores.DERCapabilities, stores.DERSettings, stores.DERStatuses, stores.DERAvailabilities,
	)

	// Scoped DER list per device
	mux.HandleFunc("GET /edev/{id}/der", scopedListHandler[sep2.DER, sep2.DERList](
		stores.DERs, handler.BuildDERList, 900,
	))
	mux.HandleFunc("GET /edev/{id}/der/{derId}/dercap", dercap)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/dercap", dercap)
	mux.HandleFunc("GET /edev/{id}/der/{derId}/derg", derg)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/derg", derg)
	mux.HandleFunc("GET /edev/{id}/der/{derId}/ders", ders)
	mux.HandleFunc("PUT /edev/{id}/der/{derId}/ders", ders)
	mux.HandleFunc("GET /edev/{id}/der/{derId}/dera", dera)

	// DERProgram under FSA
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp", scopedListHandler[sep2.DERProgram, sep2.DERProgramList](
		stores.DERPrograms, handler.BuildDERProgramList, 900,
	))

	// DERControl under DERProgram
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/derc",
		scopedListHandlerDeep[sep2.DERControl, sep2.DERControlList](
			stores.DERControls, handler.BuildDERControlList, 900,
		))

	// DefaultDERControl
	mux.HandleFunc("GET /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc",
		handler.DefaultDERControlHandler(stores.DefaultDERControls))
	mux.HandleFunc("PUT /edev/{id}/fsa/{fsaId}/derp/{derpId}/dderc",
		handler.DefaultDERControlHandler(stores.DefaultDERControls))

	// Global DERCurve
	mux.HandleFunc("GET /dc", handler.ListHandler[sep2.DERCurve, sep2.DERCurveList](
		stores.DERCurves, handler.BuildDERCurveList, 900,
	))
}

// scopedListHandler creates a list handler that scopes by the {id} path value.
func scopedListHandler[T store.Copier[T], L any](
	scopedStore *memory.ScopedStore[T],
	buildList func(href string, result store.ListResult[T], pollRate uint32) L,
	pollRate uint32,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parentID := r.PathValue("id")
		st := scopedStore.ForParent(parentID)

		h := handler.ListHandler[T, L](st, buildList, pollRate)
		h.ServeHTTP(w, r)
	}
}

// scopedListHandlerDeep creates a list handler scoped by composite key id/fsaId/derpId.
func scopedListHandlerDeep[T store.Copier[T], L any](
	scopedStore *memory.ScopedStore[T],
	buildList func(href string, result store.ListResult[T], pollRate uint32) L,
	pollRate uint32,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("id") + "/" + r.PathValue("fsaId") + "/" + r.PathValue("derpId")
		st := scopedStore.ForParent(key)

		h := handler.ListHandler[T, L](st, buildList, pollRate)
		h.ServeHTTP(w, r)
	}
}

// suppress unused import
var _ = paging.DefaultLimit
