//go:build !csip_test_hooks

// Production-build stub for the test-only CSIP mutation surface. Without
// the csip_test_hooks build tag, RegisterMutationHandlers is a no-op and
// none of the mutation handler code from test_mutations.go is compiled
// into the binary.
//
// See test_mutations.go for the gated implementation. IEEE-024.

package server

import "net/http"

// RegisterMutationHandlers is the production no-op. Build with
// `-tags csip_test_hooks` to compile the real implementation.
func RegisterMutationHandlers(_ *http.ServeMux, _ *Stores) {}
