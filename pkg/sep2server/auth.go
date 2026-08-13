package sep2server

import (
	"context"
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-server-go/internal/auth"
	"github.com/GRIDAPPSD/ieee-2030_5-server-go/pkg/sep2srv/assembly"
)

// DefaultAuthPolicy returns this server's own identity and ACL enforcement,
// composed and ready to pass as [Config.Auth].
//
// This is the ONLY door onto the ACL, and it is deliberately a composed policy
// value rather than the rules behind it: an embedder can adopt this server's
// enforcement unchanged, but it cannot reach in and reshape it, and it cannot
// accidentally reimplement a second, divergent copy. The rules themselves,
// the middleware and the identity extraction all stay internal.
//
// Wrap composes the identity middleware around the ACL middleware around the
// protocol mux, in that order: the ACL decides on an identity the layer above
// it has already established.
//
// Identity adapts this server's lookup to the (lfdi, sfdi, ok) shape core
// expects. The field ORDER is load-bearing. Core feeds the second return into
// SFDIPrefix on the EndDevice POST path, so transposing the two would misroute
// the short-SFDI guard silently rather than failing.
func DefaultAuthPolicy() assembly.AuthPolicy {
	return assembly.AuthPolicy{
		Wrap: func(next http.Handler) http.Handler {
			return auth.IdentityMiddleware(auth.ACLMiddleware(auth.DefaultACLRules())(next))
		},
		Identity: func(ctx context.Context) (lfdi, sfdi string, ok bool) {
			id, ok := auth.GetIdentity(ctx)
			return id.LFDI, id.SFDI, ok
		},
		SFDIPrefix: auth.ExtractSFDIPrefix,
	}
}
