// Package identity holds the one named type for the function that reads the
// authenticated device identity off a request context.
package identity

import "context"

// Func returns the LFDI and SFDI the auth layer put on ctx, and ok=false when
// the request has no identity. The server wires auth.GetIdentity; tests supply
// a fixed identity.
type Func func(ctx context.Context) (lfdi, sfdi string, ok bool)
