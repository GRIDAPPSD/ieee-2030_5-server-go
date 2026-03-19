package auth

import (
	"context"
	"net/http"

	sepTLS "github.com/craig8/ieee-2030_5-go/internal/tls"
)

type contextKey string

const identityKey contextKey = "device-identity"

// DeviceIdentity represents a client's IEEE 2030.5 identity
// derived from their TLS client certificate.
type DeviceIdentity struct {
	SFDI string
	LFDI string
}

// IdentityMiddleware extracts the client's SFDI/LFDI from the TLS
// connection state and attaches it to the request context.
func IdentityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "client certificate required", http.StatusForbidden)
			return
		}

		cert := r.TLS.PeerCertificates[0]
		identity := DeviceIdentity{
			SFDI: sepTLS.SFDI(cert),
			LFDI: sepTLS.LFDI(cert),
		}

		ctx := context.WithValue(r.Context(), identityKey, identity)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetIdentity retrieves the DeviceIdentity from the request context.
func GetIdentity(ctx context.Context) (DeviceIdentity, bool) {
	id, ok := ctx.Value(identityKey).(DeviceIdentity)
	return id, ok
}

// IdentityContextKey returns the context key used for DeviceIdentity.
// Exported for test helpers that need to inject identity into context.
func IdentityContextKey() contextKey {
	return identityKey
}
