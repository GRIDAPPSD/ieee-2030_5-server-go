package server

import (
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
	"github.com/craig8/ieee-2030_5-go/pkg/store/memory"
)

// Stores holds all resource stores for the server.
type Stores struct {
	EndDevices          store.EndDeviceStore
	MirrorUsagePoints   *memory.Store[sep2.MirrorUsagePoint]
	MirrorMeterReadings *memory.ScopedStore[sep2.MirrorMeterReading]
}

// NewRouter creates the HTTP router for the protocol listener.
func NewRouter(cfg *config.Config, stores *Stores, svc *handler.AdminCertService, serverSFDI, serverLFDI string) http.Handler {
	top := http.NewServeMux()

	// Protocol endpoints
	protocolMux := http.NewServeMux()
	protocolMux.HandleFunc("GET /dcap", handler.HandleDeviceCapability())
	protocolMux.HandleFunc("GET /tm", handler.HandleTime(cfg))
	protocolMux.HandleFunc("GET /sdev", handler.HandleSelfDevice(serverSFDI, serverLFDI))

	if stores != nil {
		// EndDevice endpoints
		protocolMux.HandleFunc("GET /edev", handler.ListHandler[sep2.EndDevice, sep2.EndDeviceList](
			stores.EndDevices, handler.BuildEndDeviceList, 900,
		))
		protocolMux.HandleFunc("POST /edev", handler.HandleCreateEndDevice(stores.EndDevices))
		protocolMux.HandleFunc("GET /edev/{id}", handler.HandleEndDevice(stores.EndDevices))
		protocolMux.HandleFunc("PUT /edev/{id}", handler.HandleUpdateEndDevice(stores.EndDevices))
		protocolMux.HandleFunc("DELETE /edev/{id}", handler.HandleDeleteEndDevice(stores.EndDevices))

		// Mirror Usage Point endpoints (GridAPPS-D inverter data reporting)
		if stores.MirrorUsagePoints != nil {
			protocolMux.HandleFunc("GET /mup", handler.ListHandler[sep2.MirrorUsagePoint, sep2.MirrorUsagePointList](
				stores.MirrorUsagePoints, handler.BuildMirrorUsagePointList, 300,
			))
			protocolMux.HandleFunc("POST /mup", handler.HandleCreateMirrorUsagePoint(stores.MirrorUsagePoints))
			protocolMux.HandleFunc("GET /mup/{id}", handler.HandleMirrorUsagePoint(stores.MirrorUsagePoints))
			protocolMux.HandleFunc("POST /mup/{id}/mr", handler.HandlePostMirrorMeterReading(
				stores.MirrorUsagePoints, stores.MirrorMeterReadings,
			))
		}
	}

	// Wrap protocol routes: identity extraction → ACL check → handler
	aclRules := auth.DefaultACLRules()
	protocolChain := auth.IdentityMiddleware(auth.ACLMiddleware(aclRules)(protocolMux))

	top.Handle("/dcap", protocolChain)
	top.Handle("/tm", protocolChain)
	top.Handle("/sdev", protocolChain)
	top.Handle("/edev", protocolChain)
	top.Handle("/edev/", protocolChain)
	top.Handle("/mup", protocolChain)
	top.Handle("/mup/", protocolChain)

	// Admin API on protocol port (mTLS with admin OID)
	if svc != nil {
		adminMux := http.NewServeMux()
		adminMux.HandleFunc("GET /api/certs/ca", svc.HandleGetCA())
		adminMux.HandleFunc("POST /api/certs/server", svc.HandleCreateServerCert())
		adminMux.HandleFunc("POST /api/certs/device", svc.HandleCreateDeviceCert())
		top.Handle("/api/", auth.AdminAuthMiddleware(cfg.AdminKey)(adminMux))
	}

	return top
}
