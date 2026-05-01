package server

import (
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/config"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/encoding"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/paging"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store/memory"
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

	// Server-side metering
	UsagePoints   *memory.Store[sep2.UsagePoint]
	MeterReadings *memory.ScopedStore[sep2.MeterReading]
	Readings      *memory.ScopedStore[sep2.Reading]
	ReadingTypes  *memory.Store[sep2.ReadingType]

	// New function sets
	Configurations *memory.ScopedStore[sep2.Configuration]
	DeviceStatuses *memory.ScopedStore[sep2.DeviceStatus]
	LogEvents      *memory.ScopedStore[sep2.LogEvent]
	PowerStatuses  *memory.ScopedStore[sep2.PowerStatus]
	MessagingPrograms *memory.Store[sep2.MessagingProgram]
	TextMessages      *memory.ScopedStore[sep2.TextMessage]
	FlowReservationRequests  *memory.ScopedStore[sep2.FlowReservationRequest]
	FlowReservationResponses *memory.ScopedStore[sep2.FlowReservationResponse]
	ResponseSets  *memory.Store[sep2.ResponseSet]
	Responses     *memory.ScopedStore[sep2.Response]
}

// NewRouter creates the HTTP router for the protocol listener.
func NewRouter(cfg *config.Config, stores *Stores, svc *handler.AdminCertService, serverSFDI, serverLFDI string) http.Handler {
	top := http.NewServeMux()

	protocolMux := http.NewServeMux()
	protocolMux.HandleFunc("GET /dcap", handler.HandleDeviceCapability())
	protocolMux.HandleFunc("GET /tm", handler.HandleTime(cfg))
	protocolMux.HandleFunc("GET /sdev", handler.HandleSelfDevice(serverSFDI, serverLFDI))
	protocolMux.HandleFunc("GET /sdev/sdi", handler.HandleDeviceInformation(serverLFDI))

	if stores != nil {
		registerEndDeviceRoutes(protocolMux, stores)
		registerMirrorRoutes(protocolMux, stores)
		registerDERRoutes(protocolMux, stores)
		registerMeteringRoutes(protocolMux, stores)
		registerNewFunctionSetRoutes(protocolMux, stores)
	}

	aclRules := auth.DefaultACLRules()
	protocolChain := auth.IdentityMiddleware(auth.ACLMiddleware(aclRules)(protocolMux))

	for _, prefix := range []string{
		"/dcap", "/tm", "/sdev", "/sdev/",
		"/edev", "/edev/",
		"/mup", "/mup/",
		"/dc", "/dc/",
		"/upt", "/upt/", "/rt", "/rt/",
		"/msg", "/msg/",
		"/rsps", "/rsps/",
	} {
		top.Handle(prefix, protocolChain)
	}

	if svc != nil {
		adminMux := http.NewServeMux()
		adminMux.HandleFunc("GET /api/certs/ca", svc.HandleGetCA())
		adminMux.HandleFunc("POST /api/certs/server", svc.HandleCreateServerCert())
		adminMux.HandleFunc("POST /api/certs/device", svc.HandleCreateDeviceCert())
		top.Handle("/api/", auth.AdminAuthMiddleware(cfg.AdminKey, nil)(adminMux))
	}

	// Wrap entire router with namespace detection — rewrites XML output
	// for 2013 clients (EPRI reference client) automatically
	return encoding.NamespaceMiddleware(top)
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

func registerMeteringRoutes(mux *http.ServeMux, stores *Stores) {
	if stores.UsagePoints == nil {
		return
	}
	mux.HandleFunc("GET /upt", handler.ListHandler[sep2.UsagePoint, sep2.UsagePointList](
		stores.UsagePoints, handler.BuildUsagePointList, 900,
	))
	mux.HandleFunc("POST /upt", handler.HandleCreateUsagePoint(stores.UsagePoints))
	mux.HandleFunc("GET /upt/{uptId}", handler.HandleUsagePoint(stores.UsagePoints))

	// MeterReadings scoped under UsagePoint
	mux.HandleFunc("GET /upt/{uptId}/mr", scopedListHandler[sep2.MeterReading, sep2.MeterReadingList](
		stores.MeterReadings, handler.BuildMeterReadingList, 900,
	))

	// Readings scoped under MeterReading (deep: uptId/mrId)
	mux.HandleFunc("GET /upt/{uptId}/mr/{mrId}/r", func(w http.ResponseWriter, r *http.Request) {
		key := r.PathValue("uptId") + "/" + r.PathValue("mrId")
		st := stores.Readings.ForParent(key)
		h := handler.ListHandler[sep2.Reading, sep2.ReadingList](st, handler.BuildReadingList, 900)
		h.ServeHTTP(w, r)
	})

	// ReadingTypes (global)
	mux.HandleFunc("GET /rt", handler.ListHandler[sep2.ReadingType, sep2.ReadingTypeList](
		stores.ReadingTypes, handler.BuildReadingTypeList, 900,
	))
	mux.HandleFunc("GET /rt/{id}", handler.HandleReadingType(stores.ReadingTypes))
}

func registerNewFunctionSetRoutes(mux *http.ServeMux, stores *Stores) {
	// Device sub-resources (all scoped under /edev/{id})
	if stores.Configurations != nil {
		mux.HandleFunc("GET /edev/{id}/cfg", handler.HandleConfiguration(stores.Configurations))
		mux.HandleFunc("PUT /edev/{id}/cfg", handler.HandleConfiguration(stores.Configurations))
	}
	if stores.DeviceStatuses != nil {
		mux.HandleFunc("GET /edev/{id}/dstat", handler.HandleSingletonGetPut[sep2.DeviceStatus](
			stores.DeviceStatuses,
			func(r *http.Request) string { return r.PathValue("id") },
			func(r *http.Request) sep2.DeviceStatus {
				ds := sep2.DeviceStatus{}
				ds.Href = "/edev/" + r.PathValue("id") + "/dstat"
				return ds
			},
		))
		mux.HandleFunc("PUT /edev/{id}/dstat", handler.HandleSingletonGetPut[sep2.DeviceStatus](
			stores.DeviceStatuses,
			func(r *http.Request) string { return r.PathValue("id") },
			func(r *http.Request) sep2.DeviceStatus {
				ds := sep2.DeviceStatus{}
				ds.Href = "/edev/" + r.PathValue("id") + "/dstat"
				return ds
			},
		))
	}
	if stores.LogEvents != nil {
		mux.HandleFunc("GET /edev/{id}/log", scopedListHandler[sep2.LogEvent, sep2.LogEventList](
			stores.LogEvents, handler.BuildLogEventList, 900,
		))
		mux.HandleFunc("POST /edev/{id}/log", handler.HandlePostLogEvent(stores.LogEvents))
	}
	if stores.PowerStatuses != nil {
		mux.HandleFunc("GET /edev/{id}/ps", handler.HandlePowerStatus(stores.PowerStatuses))
		mux.HandleFunc("PUT /edev/{id}/ps", handler.HandlePowerStatus(stores.PowerStatuses))
	}

	// Messaging (global)
	if stores.MessagingPrograms != nil {
		mux.HandleFunc("GET /msg", handler.ListHandler[sep2.MessagingProgram, sep2.MessagingProgramList](
			stores.MessagingPrograms, handler.BuildMessagingProgramList, 900,
		))
		mux.HandleFunc("GET /msg/{msgId}", handler.HandleMessagingProgram(stores.MessagingPrograms))
		mux.HandleFunc("GET /msg/{msgId}/tm", scopedListHandler[sep2.TextMessage, sep2.TextMessageList](
			stores.TextMessages, handler.BuildTextMessageList, 900,
		))
		mux.HandleFunc("POST /msg/{msgId}/tm", handler.HandlePostTextMessage(stores.TextMessages))
	}

	// Flow Reservation (scoped under device)
	if stores.FlowReservationRequests != nil {
		mux.HandleFunc("GET /edev/{id}/frq", scopedListHandler[sep2.FlowReservationRequest, sep2.FlowReservationRequestList](
			stores.FlowReservationRequests, handler.BuildFlowReservationRequestList, 900,
		))
		mux.HandleFunc("POST /edev/{id}/frq", handler.HandlePostFlowReservationRequest(
			stores.FlowReservationRequests, stores.FlowReservationResponses,
		))
		mux.HandleFunc("GET /edev/{id}/frp", scopedListHandler[sep2.FlowReservationResponse, sep2.FlowReservationResponseList](
			stores.FlowReservationResponses, handler.BuildFlowReservationResponseList, 900,
		))
	}

	// Response Sets (global)
	if stores.ResponseSets != nil {
		mux.HandleFunc("GET /rsps", handler.ListHandler[sep2.ResponseSet, sep2.ResponseSetList](
			stores.ResponseSets, handler.BuildResponseSetList, 900,
		))
		mux.HandleFunc("GET /rsps/{rspsId}/rsp", scopedListHandler[sep2.Response, sep2.ResponseList](
			stores.Responses, handler.BuildResponseList, 900,
		))
		mux.HandleFunc("POST /rsps/{rspsId}/rsp", handler.HandlePostResponse(stores.Responses))
	}
}

// suppress unused import
var _ = paging.DefaultLimit
