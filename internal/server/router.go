package server

import (
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
	"github.com/craig8/ieee-2030_5-go/pkg/sep2"
	"github.com/craig8/ieee-2030_5-go/pkg/store"
)

// Stores holds all resource stores for the server.
type Stores struct {
	EndDevices store.EndDeviceStore
}

// NewRouter creates the HTTP router for the protocol listener.
func NewRouter(cfg *config.Config, stores *Stores, svc *handler.AdminCertService, serverSFDI, serverLFDI string) http.Handler {
	top := http.NewServeMux()

	// Protocol endpoints
	protocolMux := http.NewServeMux()
	protocolMux.HandleFunc("GET /dcap", handler.HandleDeviceCapability())
	protocolMux.HandleFunc("GET /tm", handler.HandleTime(cfg))
	protocolMux.HandleFunc("GET /sdev", handler.HandleSelfDevice(serverSFDI, serverLFDI))

	// EndDevice endpoints
	if stores != nil {
		protocolMux.HandleFunc("GET /edev", handler.ListHandler[sep2.EndDevice, sep2.EndDeviceList](
			stores.EndDevices, handler.BuildEndDeviceList, 900,
		))
		protocolMux.HandleFunc("POST /edev", handler.HandleCreateEndDevice(stores.EndDevices))
		protocolMux.HandleFunc("GET /edev/{id}", handler.HandleEndDevice(stores.EndDevices))
		protocolMux.HandleFunc("PUT /edev/{id}", handler.HandleUpdateEndDevice(stores.EndDevices))
		protocolMux.HandleFunc("DELETE /edev/{id}", handler.HandleDeleteEndDevice(stores.EndDevices))
	}

	// Wrap protocol routes: identity extraction → ACL check → handler
	aclRules := auth.DefaultACLRules()
	protocolChain := auth.IdentityMiddleware(auth.ACLMiddleware(aclRules)(protocolMux))

	top.Handle("/dcap", protocolChain)
	top.Handle("/tm", protocolChain)
	top.Handle("/sdev", protocolChain)
	top.Handle("/edev", protocolChain)
	top.Handle("/edev/", protocolChain)

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
