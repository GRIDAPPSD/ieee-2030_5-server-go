//go:build !csip_test_hooks

// Production-build stub for the test-only CSIP mutation surface. Without
// the csip_test_hooks build tag, RegisterMutationHandlers is a no-op and
// none of the mutation handler code from test_mutations.go is compiled
// into the binary.
//
// See test_mutations.go for the gated implementation. IEEE-024.

package server

import (
	"net/http"

	"github.com/GRIDAPPSD/ieee-2030_5-go/internal/handler"
)

// RegisterMutationHandlers is the production no-op. Build with
// `-tags csip_test_hooks` to compile the real implementation. The
// notifier parameter (added by IEEE-093 so the tagged build can fan
// out Notifications from mutation hooks) is ignored here.
func RegisterMutationHandlers(_ *http.ServeMux, _ *Stores, _ handler.ResourceNotifier) {}

// wrapMutationHandlers returns h unchanged in production builds.
// Under csip_test_hooks the companion in test_mutations.go wraps h
// with an outer mux that serves /test/mutations/* and falls through
// to h for all other paths.
func wrapMutationHandlers(h http.Handler, _ *Stores, _ handler.ResourceNotifier) http.Handler {
	return h
}
