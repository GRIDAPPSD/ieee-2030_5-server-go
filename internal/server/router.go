package server

import (
	"net/http"

	"github.com/craig8/ieee-2030_5-go/internal/auth"
	"github.com/craig8/ieee-2030_5-go/internal/config"
	"github.com/craig8/ieee-2030_5-go/internal/handler"
)

// NewRouter creates the HTTP router with all IEEE 2030.5 endpoints.
// Protocol endpoints are wrapped with identity middleware.
func NewRouter(cfg *config.Config) http.Handler {
	mux := http.NewServeMux()

	// IEEE 2030.5 protocol endpoints
	mux.Handle("GET /dcap", handler.HandleDeviceCapability())
	mux.Handle("GET /tm", handler.HandleTime(cfg))

	// Wrap all protocol routes with identity extraction
	return auth.IdentityMiddleware(mux)
}
