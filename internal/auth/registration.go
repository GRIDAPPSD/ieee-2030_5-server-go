package auth

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/sep2"
	"github.com/GRIDAPPSD/ieee-2030_5-go/pkg/store"
)

// RegistrationMode controls how unknown devices are handled.
type RegistrationMode string

const (
	RegistrationModeAuto   RegistrationMode = "auto"   // auto-register on first connection
	RegistrationModeManual RegistrationMode = "manual"  // reject unknown devices
)

type contextKeyDeviceID string

const deviceIDKey contextKeyDeviceID = "device-id"

// AutoRegistrationMiddleware automatically registers devices on first mTLS connection.
// If mode is "auto", creates an EndDevice when the SFDI is not found in the store.
// If mode is "manual", unknown devices are not registered (handlers return 404 naturally).
// Attaches the device store ID to the context for downstream handlers.
func AutoRegistrationMiddleware(edevStore store.EndDeviceStore, mode RegistrationMode) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := GetIdentity(r.Context())
			if !ok {
				// No identity — let other middleware handle it
				next.ServeHTTP(w, r)
				return
			}

			// Check if device already registered
			existing, err := edevStore.GetBySFDI(r.Context(), identity.SFDI)
			if err == nil {
				// Already registered — attach device ID to context
				ctx := context.WithValue(r.Context(), deviceIDKey, extractID(existing.Href))
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			if mode != RegistrationModeAuto {
				// Manual mode — don't auto-register
				next.ServeHTTP(w, r)
				return
			}

			// Auto-register
			id := identity.SFDI[:8]
			enabled := true
			edev := sep2.EndDevice{
				SubscribableResource: sep2.SubscribableResource{
					Resource: sep2.Resource{Href: "/edev/" + id},
				},
				SFDI:        identity.SFDI,
				LFDI:        identity.LFDI,
				ChangedTime: time.Now().Unix(),
				Enabled:     &enabled,
				RegistrationLink: &sep2.Link{Href: fmt.Sprintf("/edev/%s/rg", id)},
				FunctionSetAssignmentsListLink: &sep2.ListLink{Href: fmt.Sprintf("/edev/%s/fsa", id)},
			}

			if err := edevStore.Create(r.Context(), id, edev); err != nil {
				if err == store.ErrAlreadyExists {
					// Race condition — another request registered this device
					log.Printf("auto-register: device %s already exists (race)", identity.SFDI)
				} else {
					log.Printf("auto-register: create device %s failed: %v", identity.SFDI, err)
				}
			} else {
				log.Printf("auto-register: device %s registered as /edev/%s", identity.SFDI, id)
			}

			ctx := context.WithValue(r.Context(), deviceIDKey, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// GetDeviceID retrieves the auto-registered device ID from context.
func GetDeviceID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(deviceIDKey).(string)
	return id, ok
}

func extractID(href string) string {
	for i := len(href) - 1; i >= 0; i-- {
		if href[i] == '/' {
			return href[i+1:]
		}
	}
	return href
}
